package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"payment-sim/internal/httpx"
)

const maxLedgerResponseBody = 1 << 20

type LedgerClient struct {
	baseURL string
	client  *http.Client
	token   string
}

type ledgerAccountRequest struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Currency string `json:"currency"`
}

type ledgerResource struct {
	ID string `json:"id"`
}

func NewLedgerClient(baseURL string, timeout time.Duration) *LedgerClient {
	return &LedgerClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: timeout},
	}
}

// WithToken sets the bearer token sent to the ledger; empty sends none.
func (c *LedgerClient) WithToken(token string) *LedgerClient {
	c.token = token
	return c
}

func (c *LedgerClient) Post(ctx context.Context, key string, posting LedgerPosting) error {
	for _, account := range []ledgerAccountRequest{
		{ID: customerAccount(posting.Currency), Name: "Customer payable", Currency: posting.Currency},
		{ID: pendingAccount(posting.Currency), Name: "Merchant pending", Currency: posting.Currency},
		{ID: settledAccount(posting.Currency), Name: "Merchant settled", Currency: posting.Currency},
	} {
		var result ledgerResource
		if err := c.request(ctx, http.MethodPost, "/v1/accounts", "setup:"+account.ID, account, &result); err != nil {
			return err
		}
		if result.ID == "" {
			return invalidLedgerResponse()
		}
	}
	var result ledgerResource
	if err := c.request(ctx, http.MethodPost, "/v1/postings", key, posting, &result); err != nil {
		return err
	}
	if result.ID == "" {
		return invalidLedgerResponse()
	}
	return nil
}

func (c *LedgerClient) request(ctx context.Context, method, path, key string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return httpx.Err(http.StatusInternalServerError, "internal_error", "could not encode ledger request")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return httpx.Err(http.StatusInternalServerError, "internal_error", "could not create ledger request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if id := httpx.RequestID(ctx); id != "" {
		req.Header.Set(httpx.RequestIDHeader, id)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return httpx.Err(http.StatusBadGateway, "ledger_unavailable", "ledger request failed: "+err.Error())
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := decodeLedgerResponse(response.Body, &envelope); err != nil || envelope.Error.Code == "" {
			return httpx.Err(http.StatusBadGateway, "ledger_error", fmt.Sprintf("ledger returned HTTP %d", response.StatusCode))
		}
		status := http.StatusBadGateway
		if response.StatusCode == http.StatusConflict {
			status = http.StatusConflict
		}
		return httpx.Err(status, envelope.Error.Code, "ledger: "+envelope.Error.Message)
	}
	if err := decodeLedgerResponse(response.Body, output); err != nil {
		return invalidLedgerResponse()
	}
	return nil
}

func decodeLedgerResponse(body io.Reader, output any) error {
	encoded, err := io.ReadAll(io.LimitReader(body, maxLedgerResponseBody+1))
	if err != nil {
		return err
	}
	if len(encoded) > maxLedgerResponseBody {
		return fmt.Errorf("ledger response exceeds %d bytes", maxLedgerResponseBody)
	}
	if err := rejectDuplicateLedgerObjectNames(encoded); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(encoded))
	if err := dec.Decode(output); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("ledger response must contain one JSON value")
	}
	return nil
}

func rejectDuplicateLedgerObjectNames(encoded []byte) error {
	type frame struct {
		object    bool
		expectKey bool
		keys      map[string]struct{}
	}
	var stack []frame
	completeValue := func() {
		if len(stack) == 0 {
			return
		}
		parent := &stack[len(stack)-1]
		if parent.object && !parent.expectKey {
			parent.expectKey = true
		}
	}
	dec := json.NewDecoder(bytes.NewReader(encoded))
	for {
		token, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case json.Delim:
			switch value {
			case '{':
				stack = append(stack, frame{object: true, expectKey: true, keys: make(map[string]struct{})})
			case '[':
				stack = append(stack, frame{})
			case '}', ']':
				if len(stack) == 0 {
					return fmt.Errorf("invalid ledger JSON")
				}
				stack = stack[:len(stack)-1]
				completeValue()
			}
		case string:
			if len(stack) > 0 && stack[len(stack)-1].object && stack[len(stack)-1].expectKey {
				current := &stack[len(stack)-1]
				if _, ok := current.keys[value]; ok {
					return fmt.Errorf("ledger response contains duplicate object name %q", value)
				}
				current.keys[value] = struct{}{}
				current.expectKey = false
				continue
			}
			completeValue()
		default:
			completeValue()
		}
	}
}

func invalidLedgerResponse() error {
	return httpx.Err(http.StatusBadGateway, "ledger_invalid_response", "ledger returned an invalid response")
}

package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"payment-sim/internal/httpx"
	"payment-sim/internal/ledger"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestListPaymentsPagination(t *testing.T) {
	store := NewStore(&fakeLedger{})
	for i := 0; i < 5; i++ {
		if _, err := store.Create(fmt.Sprintf("k%d", i), CreatePayment{Amount: 100, Currency: "USD"}); err != nil {
			t.Fatal(err)
		}
	}
	handler := Handler(store, quietLogger())
	type pageBody struct {
		Payments   []Payment `json:"payments"`
		NextCursor *string   `json:"next_cursor"`
	}
	getPage := func(path string, wantStatus int) pageBody {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != wantStatus {
			t.Fatalf("%s: status=%d want=%d body=%s", path, response.Code, wantStatus, response.Body.String())
		}
		var body pageBody
		if wantStatus == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		}
		return body
	}

	first := getPage("/v1/payments?limit=2", http.StatusOK)
	if len(first.Payments) != 2 || first.NextCursor == nil || *first.NextCursor == "" {
		t.Fatalf("exact-limit first page: items=%d next=%v", len(first.Payments), first.NextCursor)
	}
	seen := map[string]bool{}
	page := first
	pages := 0
	for {
		if len(page.Payments) > 2 {
			t.Fatalf("page exceeds limit: %d", len(page.Payments))
		}
		for _, payment := range page.Payments {
			if seen[payment.ID] {
				t.Fatalf("duplicate payment %s", payment.ID)
			}
			seen[payment.ID] = true
		}
		pages++
		if page.NextCursor == nil {
			if len(page.Payments) != 1 {
				t.Fatalf("terminal page items=%d, want 1", len(page.Payments))
			}
			break
		}
		page = getPage("/v1/payments?limit=2&cursor="+*page.NextCursor, http.StatusOK)
	}
	if len(seen) != 5 || pages != 3 {
		t.Fatalf("seen=%d pages=%d", len(seen), pages)
	}

	exact := getPage("/v1/payments?limit=5", http.StatusOK)
	if len(exact.Payments) != 5 || exact.NextCursor != nil {
		t.Fatalf("exact terminal page: items=%d next=%v", len(exact.Payments), exact.NextCursor)
	}
	valid := *first.NextCursor
	replacement := "A"
	if valid[0] == 'A' {
		replacement = "B"
	}
	tampered := replacement + valid[1:]
	for _, cursor := range []string{"!!!", httpx.EncodeCursor("unknown-payment"), tampered} {
		getPage("/v1/payments?limit=2&cursor="+cursor, http.StatusBadRequest)
	}
	for _, query := range []string{"limit=0", "limit=201"} {
		getPage("/v1/payments?"+query, http.StatusBadRequest)
	}
}

func TestPaymentAuthExemptions(t *testing.T) {
	handler := NewHandler(NewStore(&fakeLedger{}), quietLogger(), "tok")
	status := func(method, path, auth, body string) int {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Idempotency-Key", "auth-key")
		if auth != "" {
			request.Header.Set("Authorization", auth)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	if got := status(http.MethodGet, "/healthz", "", ""); got != http.StatusOK {
		t.Errorf("healthz=%d", got)
	}
	if got := status(http.MethodGet, "/", "", ""); got != http.StatusOK {
		t.Errorf("admin=%d", got)
	}
	create := `{"amount":5,"currency":"USD"}`
	if got := status(http.MethodPost, "/v1/payments", "", create); got != http.StatusUnauthorized {
		t.Errorf("no token=%d", got)
	}
	if got := status(http.MethodPost, "/v1/payments", "Bearer bad", create); got != http.StatusUnauthorized {
		t.Errorf("bad token=%d", got)
	}
	if got := status(http.MethodPost, "/v1/payments", "Bearer tok", create); got != http.StatusCreated {
		t.Errorf("good token=%d", got)
	}
}

func TestLedgerClientSendsToken(t *testing.T) {
	store := ledger.NewStore()
	server := httptest.NewServer(ledger.NewHandler(store, quietLogger(), "ledger-tok"))
	defer server.Close()
	posting := LedgerPosting{Currency: "USD", Description: "t", Entries: []LedgerEntry{
		{AccountID: customerAccount("USD"), Amount: -1}, {AccountID: pendingAccount("USD"), Amount: 1},
	}}
	if err := NewLedgerClient(server.URL, time.Second).Post(context.Background(), "no-token", posting); err == nil {
		t.Fatal("expected unauthorized without token")
	}
	if err := NewLedgerClient(server.URL, time.Second).WithToken("ledger-tok").Post(context.Background(), "with-token", posting); err != nil {
		t.Fatal(err)
	}
	if len(store.Postings()) != 1 {
		t.Fatalf("postings=%d", len(store.Postings()))
	}
}

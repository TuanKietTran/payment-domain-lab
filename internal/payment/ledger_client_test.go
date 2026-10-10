package payment

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"payment-sim/internal/ledger"
)

func TestLedgerClientCreatesAccountsAndPostsIdempotently(t *testing.T) {
	store := ledger.NewStore()
	server := httptest.NewServer(ledger.Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	client := NewLedgerClient(server.URL, time.Second)
	request := LedgerPosting{Currency: "USD", Description: "auth", Entries: []LedgerEntry{
		{AccountID: customerAccount("USD"), Amount: -50},
		{AccountID: pendingAccount("USD"), Amount: 50},
	}}
	if err := client.Post(context.Background(), "post-key", request); err != nil {
		t.Fatal(err)
	}
	if err := client.Post(context.Background(), "post-key", request); err != nil {
		t.Fatal(err)
	}
	if len(store.Postings()) != 1 || len(store.Accounts()) != 3 {
		t.Fatalf("postings %d, accounts %d", len(store.Postings()), len(store.Accounts()))
	}
}

func TestLedgerClientHonorsCanceledContextBeforeRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls++
	}))
	defer server.Close()
	client := NewLedgerClient(server.URL, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := client.Post(ctx, "canceled", LedgerPosting{Currency: "USD"})
	assertCode(t, err, "ledger_unavailable")
	if calls != 0 {
		t.Fatalf("canceled context still made %d calls", calls)
	}
}

func TestLedgerClientEnforcesTimeout(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	client := NewLedgerClient(server.URL, 25*time.Millisecond)
	done := make(chan error, 1)
	go func() {
		done <- client.Post(context.Background(), "timeout", LedgerPosting{Currency: "USD"})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("ledger request did not start")
	}
	select {
	case err := <-done:
		assertCode(t, err, "ledger_unavailable")
	case <-time.After(time.Second):
		t.Fatal("ledger timeout was not enforced")
	}
}

func TestLedgerClientRejectsMalformedSuccessfulResponse(t *testing.T) {
	for name, body := range map[string]string{
		"trailing JSON":  `{"id":"ledger-id"} {}`,
		"duplicate ID":   `{"id":"first","id":"second"}`,
		"missing ID":     `{}`,
		"oversized":      `{"id":"ledger-id"}` + strings.Repeat(" ", maxLedgerResponseBody),
		"oversized junk": strings.Repeat("x", maxLedgerResponseBody+1),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client := NewLedgerClient(server.URL, time.Second)
			err := client.Post(context.Background(), "key", LedgerPosting{Currency: "USD"})
			assertCode(t, err, "ledger_invalid_response")
		})
	}
}

func TestLedgerClientMapsHTTPStatusAndErrorBodies(t *testing.T) {
	for name, tc := range map[string]struct {
		status string
		body   string
		code   string
	}{
		"conflict envelope":    {status: "409 Conflict", body: `{"error":{"code":"idempotency_conflict","message":"conflict"}}`, code: "idempotency_conflict"},
		"bad gateway envelope": {status: "502 Bad Gateway", body: `{"error":{"code":"ledger_down","message":"down"}}`, code: "ledger_down"},
		"malformed error":      {status: "500 Internal Server Error", body: `not-json`, code: "ledger_error"},
		"missing error code":   {status: "400 Bad Request", body: `{"error":{"message":"bad"}}`, code: "ledger_error"},
		"trailing error json":  {status: "400 Bad Request", body: `{"error":{"code":"bad","message":"bad"}} {}`, code: "ledger_error"},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(statusCode(tc.status))
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewLedgerClient(server.URL, time.Second)
			err := client.Post(context.Background(), "key", LedgerPosting{Currency: "USD"})
			assertCode(t, err, tc.code)
		})
	}
}

func statusCode(status string) int {
	code := 0
	_, _ = fmt.Sscanf(status, "%d", &code)
	if code == 0 {
		_, _ = fmt.Sscanf(status, "%d %*s", &code)
	}
	if code == 0 {
		return http.StatusInternalServerError
	}
	return code
}

func TestLedgerClientMapsUnavailableAndMalformedResponses(t *testing.T) {
	client := NewLedgerClient("http://127.0.0.1:1", 50*time.Millisecond)
	err := client.Post(context.Background(), "key", LedgerPosting{Currency: "USD"})
	assertCode(t, err, "ledger_unavailable")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()
	client = NewLedgerClient(server.URL, time.Second)
	err = client.Post(context.Background(), "key", LedgerPosting{Currency: "USD"})
	assertCode(t, err, "ledger_invalid_response")
}

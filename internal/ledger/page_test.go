package ledger

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"payment-sim/internal/httpx"
)

func TestLedgerListPagination(t *testing.T) {
	s := NewStore()
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("acct%d", i)
		if _, err := s.CreateAccount("k-"+id, CreateAccount{ID: id, Name: id, Currency: "USD"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := s.CreatePosting(fmt.Sprintf("p%d", i), CreatePosting{Currency: "USD", Description: "x", Entries: []Entry{{AccountID: "acct0", Amount: -1}, {AccountID: "acct1", Amount: 1}}}); err != nil {
			t.Fatal(err)
		}
	}
	handler := Handler(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := func(path string, wantStatus int) map[string]json.RawMessage {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != wantStatus {
			t.Fatalf("%s: status=%d want=%d body=%s", path, response.Code, wantStatus, response.Body.String())
		}
		var body map[string]json.RawMessage
		if wantStatus == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		}
		return body
	}
	walk := func(base, key string, wantTotal int) string {
		seen := map[string]bool{}
		path := base + "?limit=2"
		pages := 0
		firstCursor := ""
		for {
			body := request(path, http.StatusOK)
			var items []struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(body[key], &items); err != nil {
				t.Fatal(err)
			}
			if len(items) > 2 {
				t.Fatalf("%s page exceeds limit: %d", base, len(items))
			}
			for _, item := range items {
				if seen[item.ID] {
					t.Fatalf("%s duplicate %s", base, item.ID)
				}
				seen[item.ID] = true
			}
			pages++
			rawCursor, hasNext := body["next_cursor"]
			if !hasNext {
				wantTerminal := wantTotal % 2
				if wantTerminal == 0 {
					wantTerminal = 2
				}
				if len(items) != wantTerminal {
					t.Fatalf("%s terminal items=%d want=%d", base, len(items), wantTerminal)
				}
				break
			}
			var cursor string
			if err := json.Unmarshal(rawCursor, &cursor); err != nil || cursor == "" {
				t.Fatalf("%s invalid next_cursor: %q (%v)", base, rawCursor, err)
			}
			if pages == 1 {
				if len(items) != 2 {
					t.Fatalf("%s exact-limit first page items=%d", base, len(items))
				}
				firstCursor = cursor
			}
			path = base + "?limit=2&cursor=" + cursor
		}
		if len(seen) != wantTotal {
			t.Fatalf("%s unique items=%d want=%d", base, len(seen), wantTotal)
		}
		return firstCursor
	}

	accountCursor := walk("/v1/accounts", "accounts", 5)
	postingCursor := walk("/v1/postings", "postings", 3)
	for _, exact := range []struct {
		path, key string
		count     int
	}{
		{"/v1/accounts?limit=5", "accounts", 5},
		{"/v1/postings?limit=3", "postings", 3},
	} {
		body := request(exact.path, http.StatusOK)
		var items []json.RawMessage
		if err := json.Unmarshal(body[exact.key], &items); err != nil || len(items) != exact.count {
			t.Fatalf("%s items=%d err=%v", exact.path, len(items), err)
		}
		if _, ok := body["next_cursor"]; ok {
			t.Fatalf("%s terminal exact-limit page has next_cursor", exact.path)
		}
	}
	for _, endpoint := range []struct {
		base, valid string
	}{
		{"/v1/accounts", accountCursor},
		{"/v1/postings", postingCursor},
	} {
		replacement := "A"
		if endpoint.valid[0] == 'A' {
			replacement = "B"
		}
		tampered := replacement + endpoint.valid[1:]
		for _, cursor := range []string{"!!!", httpx.EncodeCursor("unknown-id"), tampered} {
			request(endpoint.base+"?limit=2&cursor="+cursor, http.StatusBadRequest)
		}
	}
	for _, path := range []string{"/v1/accounts?limit=0", "/v1/postings?limit=999"} {
		request(path, http.StatusBadRequest)
	}
}

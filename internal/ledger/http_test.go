package ledger

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLedgerAPIErrorContractAndMethods(t *testing.T) {
	handler := Handler(NewStore(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	tests := []struct {
		method string
		path   string
		body   string
		status int
		code   string
		allow  string
	}{
		{method: http.MethodGet, path: "/v1/missing", status: http.StatusNotFound, code: "not_found"},
		{method: http.MethodGet, path: "/v1/accounts/missing", status: http.StatusNotFound, code: "account_not_found"},
		{method: http.MethodPost, path: "/healthz", status: http.StatusMethodNotAllowed, code: "method_not_allowed", allow: "GET"},
		{method: http.MethodDelete, path: "/v1/accounts", status: http.StatusMethodNotAllowed, code: "method_not_allowed", allow: "GET, POST"},
		{method: http.MethodPost, path: "/v1/accounts/missing", status: http.StatusMethodNotAllowed, code: "method_not_allowed", allow: "GET"},
		{method: http.MethodPatch, path: "/v1/postings", status: http.StatusMethodNotAllowed, code: "method_not_allowed", allow: "GET, POST"},
		{method: http.MethodPost, path: "/v1/accounts", body: `{}`, status: http.StatusBadRequest, code: "idempotency_key_required"},
		{method: http.MethodPost, path: "/v1/accounts", body: `{"name":"a","name":"b","currency":"USD"}`, status: http.StatusBadRequest, code: "invalid_json"},
		{method: http.MethodPost, path: "/v1/accounts", body: strings.Repeat(" ", 1<<20) + `{}`, status: http.StatusRequestEntityTooLarge, code: "request_too_large"},
		{method: http.MethodPost, path: "/v1/postings", body: `{} {}`, status: http.StatusBadRequest, code: "invalid_json"},
		{method: http.MethodPost, path: "/v1/postings", body: `{"currency":"USD","entries":[{"account_id":"a","amount":1,"amount":2},{"account_id":"a","amount":-2}]}`, status: http.StatusBadRequest, code: "invalid_json"},
		{method: http.MethodPost, path: "/v1/postings", body: strings.Repeat(" ", 1<<20) + `{}`, status: http.StatusRequestEntityTooLarge, code: "request_too_large"},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("X-Request-ID", "ledger-contract")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			var envelope struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("returned non-JSON %q: %v", response.Body.String(), err)
			}
			if response.Code != test.status || envelope.Error.Code != test.code || envelope.Error.Message == "" {
				t.Errorf("status=%d error=%+v, want %d %q", response.Code, envelope.Error, test.status, test.code)
			}
			if got := response.Header().Get("Allow"); got != test.allow {
				t.Errorf("Allow=%q, want %q", got, test.allow)
			}
			if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Errorf("Content-Type=%q", got)
			}
			if response.Header().Get("X-Request-ID") != "ledger-contract" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("middleware headers=%v", response.Header())
			}
		})
	}
}

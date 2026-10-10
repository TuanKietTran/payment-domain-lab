package payment

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminPageHasAccessibleOperationalControls(t *testing.T) {
	handler := Handler(NewStore(&fakeLedger{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("status=%d content-type=%q", response.Code, response.Header().Get("Content-Type"))
	}
	for _, header := range []string{"Content-Security-Policy", "Referrer-Policy", "X-Frame-Options", "X-Content-Type-Options"} {
		if response.Header().Get(header) == "" {
			t.Errorf("admin response missing %s", header)
		}
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control=%q", response.Header().Get("Cache-Control"))
	}
	for _, directive := range []string{"default-src 'self'", "object-src 'none'", "base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(response.Header().Get("Content-Security-Policy"), directive) {
			t.Errorf("CSP missing %q", directive)
		}
	}
	for _, required := range []string{
		"<main>", "aria-live=\"polite\"", "aria-atomic=\"true\"", "aria-busy=\"true\"",
		"<label for=\"amount\">", "<label for=\"currency\">", "<label for=\"description\">", "<label for=\"token\">",
		"aria-describedby=\"token-help\"", "Apply token", "id=\"refresh\"", "id=\"more\"", "data-action=\"refund\"",
		"sessionStorage.getItem('apiToken')", "sessionStorage.setItem('apiToken', tokenEl.value)", "Authorization:`Bearer ${token}`",
		"if (!append) { nextCursor = ''; moreEl.hidden = true; }", "insertAdjacentHTML('beforeend', cards)",
		"if (!paymentsEl.querySelector('article'))", "moreEl.disabled = true", "moreEl.disabled = false",
		"if (resultTitle.textContent === 'Load failed') resultEl.hidden = true;",
		"finally { if (button.isConnected) button.disabled = false; }", "escapeHTML(payment.amount)", "escapeHTML(payment.refunded_amount)",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("admin page missing %q", required)
		}
	}
}

func TestPaymentAPIErrorContractAndMethods(t *testing.T) {
	handler := Handler(NewStore(&fakeLedger{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	tests := []struct {
		method string
		path   string
		body   string
		status int
		code   string
		allow  string
	}{
		{method: http.MethodGet, path: "/v1/missing", status: http.StatusNotFound, code: "not_found"},
		{method: http.MethodGet, path: "/not-a-page", status: http.StatusNotFound, code: "not_found"},
		{method: http.MethodGet, path: "/v1/payments/missing", status: http.StatusNotFound, code: "payment_not_found"},
		{method: http.MethodPost, path: "/", status: http.StatusMethodNotAllowed, code: "method_not_allowed", allow: "GET"},
		{method: http.MethodPost, path: "/healthz", status: http.StatusMethodNotAllowed, code: "method_not_allowed", allow: "GET"},
		{method: http.MethodDelete, path: "/v1/payments", status: http.StatusMethodNotAllowed, code: "method_not_allowed", allow: "GET, POST"},
		{method: http.MethodPost, path: "/v1/payments/missing", status: http.StatusMethodNotAllowed, code: "method_not_allowed", allow: "GET"},
		{method: http.MethodGet, path: "/v1/payments/missing/actions/capture", status: http.StatusMethodNotAllowed, code: "method_not_allowed", allow: "POST"},
		{method: http.MethodPost, path: "/v1/payments", body: `{}`, status: http.StatusBadRequest, code: "idempotency_key_required"},
		{method: http.MethodPost, path: "/v1/payments", body: `{"amount":1,"currency":"USD"} {}`, status: http.StatusBadRequest, code: "invalid_json"},
		{method: http.MethodPost, path: "/v1/payments", body: `{"amount":1,"amount":2,"currency":"USD"}`, status: http.StatusBadRequest, code: "invalid_json"},
		{method: http.MethodPost, path: "/v1/payments", body: `{"amount":1,"currency":"USD","metadata":{"x":1,"x":2}}`, status: http.StatusBadRequest, code: "invalid_json"},
		{method: http.MethodPost, path: "/v1/payments", body: strings.Repeat(" ", 1<<20) + `{}`, status: http.StatusRequestEntityTooLarge, code: "request_too_large"},
		{method: http.MethodPost, path: "/v1/payments/missing/actions/refund", body: `{"amount":1,"amount":2}`, status: http.StatusBadRequest, code: "invalid_json"},
		{method: http.MethodPost, path: "/v1/payments/missing/actions/refund", body: strings.Repeat(" ", 1<<20) + `{}`, status: http.StatusRequestEntityTooLarge, code: "request_too_large"},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		request.Header.Set("X-Request-ID", "payment-contract")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("%s %s returned non-JSON %q: %v", test.method, test.path, response.Body.String(), err)
		}
		if response.Code != test.status || envelope.Error.Code != test.code {
			t.Errorf("%s %s status=%d code=%q, want %d %q", test.method, test.path, response.Code, envelope.Error.Code, test.status, test.code)
		}
		if got := response.Header().Get("Allow"); got != test.allow {
			t.Errorf("%s %s Allow=%q, want %q", test.method, test.path, got, test.allow)
		}
		if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("%s %s Content-Type=%q", test.method, test.path, got)
		}
		if response.Header().Get("X-Request-ID") != "payment-contract" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s %s middleware headers=%v", test.method, test.path, response.Header())
		}
	}
}

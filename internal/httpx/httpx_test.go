package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiddlewarePreservesHandlerResponse(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := Middleware(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("X-Handler", "one")
		w.Header().Add("X-Handler", "two")
		w.Header().Set(RequestIDHeader, "handler-spoof")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("handler body"))
	}))
	request := httptest.NewRequest(http.MethodGet, "/preserved", nil)
	request.Header.Set(RequestIDHeader, "caller-id")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Body.String() != "handler body" {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	if got := response.Header().Values("X-Handler"); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("handler headers=%v", got)
	}
	if got := response.Header().Get(RequestIDHeader); got != "caller-id" {
		t.Fatalf("request ID=%q", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options=%q", got)
	}
}

func TestMiddlewareIgnoresDuplicateWriteHeader(t *testing.T) {
	handler := Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("created"))
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))
	if response.Code != http.StatusCreated || response.Body.String() != "created" {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestMiddlewareRecoversPanicAsCleanJSON(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := Middleware(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Partial", "must-not-leak")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("partial"))
		panic("boom")
	}))
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	request.Header.Set(RequestIDHeader, "panic-id")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d content-type=%q body=%q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	if response.Header().Get("X-Partial") != "" || strings.Contains(response.Body.String(), "partial") {
		t.Fatalf("partial response leaked: headers=%v body=%q", response.Header(), response.Body.String())
	}
	if response.Header().Get(RequestIDHeader) != "panic-id" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("middleware headers=%v", response.Header())
	}
	var body map[string]map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["error"]["code"] != "internal_error" {
		t.Fatalf("body=%q error=%v", response.Body.String(), err)
	}
}

func TestMiddlewareAddsRequestIDToErrorResponse(t *testing.T) {
	handler := Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, Err(http.StatusBadRequest, "bad_request", "bad request"))
	}))
	request := httptest.NewRequest(http.MethodGet, "/error", nil)
	request.Header.Set(RequestIDHeader, "error-id")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || response.Header().Get(RequestIDHeader) != "error-id" {
		t.Fatalf("status=%d request-id=%q", response.Code, response.Header().Get(RequestIDHeader))
	}
}

func TestDecodeObjectAndSizeBoundaries(t *testing.T) {
	prefix, suffix := `{"value":"`, `"}`
	exact := prefix + strings.Repeat("x", maxRequestBody-len(prefix)-len(suffix)) + suffix
	if len(exact) != maxRequestBody {
		t.Fatalf("exact body length=%d", len(exact))
	}
	var destination struct {
		Value string `json:"value"`
	}
	if err := Decode(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(exact)), &destination); err != nil {
		t.Fatalf("exact-limit object rejected: %v", err)
	}
	if len(destination.Value) != maxRequestBody-len(prefix)-len(suffix) {
		t.Fatalf("decoded value length=%d", len(destination.Value))
	}

	tests := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"one byte over", prefix + strings.Repeat("x", maxRequestBody-len(prefix)-len(suffix)+1) + suffix, http.StatusRequestEntityTooLarge, "request_too_large"},
		{"oversized trailing whitespace", `{}` + strings.Repeat(" ", maxRequestBody), http.StatusRequestEntityTooLarge, "request_too_large"},
		{"two objects", `{} {}`, http.StatusBadRequest, "invalid_json"},
		{"duplicate key", `{"value":"first","value":"second"}`, http.StatusBadRequest, "invalid_json"},
		{"nested duplicate key", `{"value":"ok","nested":{"id":1,"id":2}}`, http.StatusBadRequest, "invalid_json"},
		{"unknown field", `{"unknown":true}`, http.StatusBadRequest, "invalid_json"},
		{"empty", ``, http.StatusBadRequest, "invalid_json"},
		{"null", `null`, http.StatusBadRequest, "invalid_json"},
		{"array", `[]`, http.StatusBadRequest, "invalid_json"},
		{"string", `"value"`, http.StatusBadRequest, "invalid_json"},
		{"number", `1`, http.StatusBadRequest, "invalid_json"},
		{"boolean", `true`, http.StatusBadRequest, "invalid_json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			var value struct {
				Value string `json:"value"`
			}
			err := Decode(httptest.NewRecorder(), request, &value)
			var api *APIError
			if !errors.As(err, &api) || api.Status != test.status || api.Code != test.code {
				t.Fatalf("error=%v, want status=%d code=%s", err, test.status, test.code)
			}
		})
	}
}

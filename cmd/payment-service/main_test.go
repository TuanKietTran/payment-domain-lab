package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"payment-sim/internal/ledger"
	"payment-sim/internal/payment"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" || cfg.LedgerURL != "http://127.0.0.1:8081" || cfg.LedgerTimeout != 5*time.Second || cfg.AuthToken != "" || cfg.LedgerToken != "" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"PAYMENT_ADDR": "127.0.0.1:9", "LEDGER_URL": "http://ledger:8081", "PAYMENT_LEDGER_TIMEOUT": "750ms",
		"PAYMENT_AUTH_TOKEN": "inbound", "LEDGER_AUTH_TOKEN": "outbound", "SHUTDOWN_TIMEOUT": "1s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:9" || cfg.LedgerURL != "http://ledger:8081" || cfg.LedgerTimeout != 750*time.Millisecond ||
		cfg.AuthToken != "inbound" || cfg.LedgerToken != "outbound" || cfg.ShutdownTimeout != time.Second {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadConfigRejectsBadDurations(t *testing.T) {
	for _, name := range []string{"PAYMENT_LEDGER_TIMEOUT", "HTTP_WRITE_TIMEOUT", "SHUTDOWN_TIMEOUT"} {
		for _, value := range []string{"abc", "0", "-1s"} {
			if _, err := loadConfig(env(map[string]string{name: value})); err == nil {
				t.Errorf("%s=%q: expected error", name, value)
			}
		}
	}
}

func start(t *testing.T, cfg config) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addr := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), func(a net.Addr) { addr <- a })
	}()
	select {
	case a := <-addr:
		return "http://" + a.String(), cancel, done
	case err := <-done:
		cancel()
		t.Fatalf("run exited early: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not start")
	}
	return "", cancel, done
}

func stop(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
}

func get(t *testing.T, url, auth string) *http.Response {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, url, nil)
	if auth != "" {
		request.Header.Set("Authorization", auth)
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	return response
}

func TestRunServesAdminAndShutsDown(t *testing.T) {
	cfg, _ := loadConfig(env(map[string]string{"PAYMENT_ADDR": "127.0.0.1:0"}))
	base, cancel, done := start(t, cfg)
	if got := get(t, base+"/", "").StatusCode; got != http.StatusOK {
		t.Errorf("admin = %d", got)
	}
	response := get(t, base+"/healthz", "")
	if response.StatusCode != http.StatusOK || response.Header.Get("X-Request-ID") == "" {
		t.Errorf("healthz status=%d request-id=%q", response.StatusCode, response.Header.Get("X-Request-ID"))
	}
	if got := get(t, base+"/v1/payments", "").StatusCode; got != http.StatusOK {
		t.Errorf("auth-off list = %d", got)
	}
	stop(t, cancel, done)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	if response, err := client.Get(base + "/healthz"); err == nil {
		response.Body.Close()
		t.Fatal("server still accepted requests after shutdown")
	}
}

func TestRunWiresAuthToken(t *testing.T) {
	cfg, _ := loadConfig(env(map[string]string{"PAYMENT_ADDR": "127.0.0.1:0", "PAYMENT_AUTH_TOKEN": "tok"}))
	base, cancel, done := start(t, cfg)
	defer stop(t, cancel, done)
	if got := get(t, base+"/healthz", "").StatusCode; got != http.StatusOK {
		t.Errorf("healthz = %d", got)
	}
	if got := get(t, base+"/", "").StatusCode; got != http.StatusOK {
		t.Errorf("admin page = %d", got)
	}
	if got := get(t, base+"/v1/payments", "").StatusCode; got != http.StatusUnauthorized {
		t.Errorf("no token = %d", got)
	}
	if got := get(t, base+"/v1/payments", "Bearer tok").StatusCode; got != http.StatusOK {
		t.Errorf("right token = %d", got)
	}
}

func TestRunPropagatesRequestIDAndLedgerAuth(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ledgerStore := ledger.NewStore()
	ledgerHandler := ledger.NewHandler(ledgerStore, logger, "ledger-secret")
	var mu sync.Mutex
	var ledgerHeaders []http.Header
	ledgerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ledgerHeaders = append(ledgerHeaders, r.Header.Clone())
		mu.Unlock()
		ledgerHandler.ServeHTTP(w, r)
	}))
	defer ledgerServer.Close()

	cfg, err := loadConfig(env(map[string]string{
		"PAYMENT_ADDR":       "127.0.0.1:0",
		"PAYMENT_AUTH_TOKEN": "payment-secret",
		"LEDGER_URL":         ledgerServer.URL,
		"LEDGER_AUTH_TOKEN":  "ledger-secret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	base, cancel, done := start(t, cfg)
	defer stop(t, cancel, done)

	post := func(path, key, body string, output any) *http.Response {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, base+path, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", key)
		request.Header.Set("Authorization", "Bearer payment-secret")
		request.Header.Set("X-Request-ID", "round3-e2e-id")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			body, _ := io.ReadAll(response.Body)
			t.Fatalf("POST %s: status=%d body=%s", path, response.StatusCode, body)
		}
		if response.Header.Get("X-Request-ID") != "round3-e2e-id" {
			t.Fatalf("POST %s: response request ID = %q", path, response.Header.Get("X-Request-ID"))
		}
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
		return response
	}

	var created payment.Payment
	post("/v1/payments", "create-e2e", `{"amount":100,"currency":"USD"}`, &created)
	post("/v1/payments/"+created.ID+"/actions/authorize", "authorize-e2e", `{}`, &payment.Payment{})

	mu.Lock()
	defer mu.Unlock()
	if len(ledgerHeaders) != 4 {
		t.Fatalf("ledger requests=%d, want 4", len(ledgerHeaders))
	}
	for i, header := range ledgerHeaders {
		if got := header.Get("Authorization"); got != "Bearer ledger-secret" {
			t.Errorf("ledger request %d Authorization=%q", i, got)
		}
		if got := header.Get("X-Request-ID"); got != "round3-e2e-id" {
			t.Errorf("ledger request %d X-Request-ID=%q", i, got)
		}
	}
	if len(ledgerStore.Postings()) != 1 {
		t.Fatalf("ledger postings=%d", len(ledgerStore.Postings()))
	}
}

func TestRunReportsListenError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg, _ := loadConfig(env(map[string]string{"PAYMENT_ADDR": listener.Addr().String()}))
	if err := run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil); err == nil {
		t.Fatal("expected listen error")
	}
}

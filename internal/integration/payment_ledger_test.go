package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"payment-sim/internal/ledger"
	"payment-sim/internal/payment"
)

type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestPaymentLifecycleAcrossRealHTTPHandlers(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ledgerStore := ledger.NewStore()
	ledgerServer := httptest.NewServer(ledger.Handler(ledgerStore, logger))
	defer ledgerServer.Close()
	paymentStore := payment.NewStore(payment.NewLedgerClient(ledgerServer.URL, time.Second))
	paymentServer := httptest.NewServer(payment.Handler(paymentStore, logger))
	defer paymentServer.Close()

	var created payment.Payment
	doJSON(t, http.MethodPost, paymentServer.URL+"/v1/payments", "create", map[string]any{"amount": 1000, "currency": "usd", "description": "integration"}, http.StatusCreated, &created)
	doJSON(t, http.MethodPost, paymentServer.URL+"/v1/payments/"+created.ID+"/actions/authorize", "auth", map[string]any{}, http.StatusOK, &payment.Payment{})
	doJSON(t, http.MethodPost, paymentServer.URL+"/v1/payments/"+created.ID+"/actions/capture", "capture", map[string]any{}, http.StatusOK, &payment.Payment{})
	var partial payment.Payment
	doJSON(t, http.MethodPost, paymentServer.URL+"/v1/payments/"+created.ID+"/actions/refund", "refund-1", map[string]any{"amount": 250}, http.StatusOK, &partial)
	if partial.Status != payment.StatusPartiallyRefunded || partial.RefundedAmount != 250 {
		t.Fatalf("partial refund = %+v", partial)
	}
	var final payment.Payment
	doJSON(t, http.MethodPost, paymentServer.URL+"/v1/payments/"+created.ID+"/actions/refund", "refund-2", map[string]any{"amount": 750}, http.StatusOK, &final)
	if final.Status != payment.StatusRefunded || final.RefundedAmount != 1000 {
		t.Fatalf("final refund = %+v", final)
	}

	accounts := make(map[string]ledger.Account)
	for _, account := range ledgerStore.Accounts() {
		accounts[account.ID] = account
	}
	if accounts["customer_payable_USD"].Balance != 0 || accounts["merchant_pending_USD"].Balance != 0 || accounts["merchant_settled_USD"].Balance != 0 {
		t.Fatalf("unexpected final balances: %+v", accounts)
	}
	if len(ledgerStore.Postings()) != 4 {
		t.Fatalf("got %d postings, want 4", len(ledgerStore.Postings()))
	}

	var invalid errorEnvelope
	doJSON(t, http.MethodPost, paymentServer.URL+"/v1/payments/"+created.ID+"/actions/capture", "invalid-capture", map[string]any{}, http.StatusConflict, &invalid)
	if invalid.Error.Code != "invalid_transition" || len(ledgerStore.Postings()) != 4 {
		t.Fatalf("invalid transition = %+v; postings=%d", invalid, len(ledgerStore.Postings()))
	}
}

func TestLedgerOverflowLeavesPaymentAtomicAndRetryable(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ledgerStore := ledger.NewStore()
	ledgerServer := httptest.NewServer(ledger.Handler(ledgerStore, logger))
	defer ledgerServer.Close()
	paymentStore := payment.NewStore(payment.NewLedgerClient(ledgerServer.URL, time.Second))
	paymentServer := httptest.NewServer(payment.Handler(paymentStore, logger))
	defer paymentServer.Close()

	var maximum, one payment.Payment
	doJSON(t, http.MethodPost, paymentServer.URL+"/v1/payments", "create-max", map[string]any{"amount": math.MaxInt64, "currency": "USD"}, http.StatusCreated, &maximum)
	doJSON(t, http.MethodPost, paymentServer.URL+"/v1/payments", "create-one", map[string]any{"amount": 1, "currency": "USD"}, http.StatusCreated, &one)
	doJSON(t, http.MethodPost, paymentServer.URL+"/v1/payments/"+maximum.ID+"/actions/authorize", "authorize-max", map[string]any{}, http.StatusOK, &payment.Payment{})
	var overflow errorEnvelope
	doJSON(t, http.MethodPost, paymentServer.URL+"/v1/payments/"+one.ID+"/actions/authorize", "authorize-one", map[string]any{}, http.StatusBadGateway, &overflow)
	if overflow.Error.Code != "balance_overflow" {
		t.Fatalf("overflow=%+v", overflow)
	}
	unchanged, _ := paymentStore.Get(one.ID)
	if unchanged.Status != payment.StatusCreated || len(ledgerStore.Postings()) != 1 {
		t.Fatalf("failed authorization payment=%+v postings=%d", unchanged, len(ledgerStore.Postings()))
	}

	doJSON(t, http.MethodPost, paymentServer.URL+"/v1/payments/"+maximum.ID+"/actions/cancel", "cancel-max", map[string]any{}, http.StatusOK, &payment.Payment{})
	var recovered payment.Payment
	doJSON(t, http.MethodPost, paymentServer.URL+"/v1/payments/"+one.ID+"/actions/authorize", "authorize-one", map[string]any{}, http.StatusOK, &recovered)
	if recovered.Status != payment.StatusAuthorized || len(ledgerStore.Postings()) != 3 {
		t.Fatalf("recovered=%+v postings=%d", recovered, len(ledgerStore.Postings()))
	}
}

func TestHTTPIdempotencyAndValidationAcrossServices(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ledgerStore := ledger.NewStore()
	ledgerServer := httptest.NewServer(ledger.Handler(ledgerStore, logger))
	defer ledgerServer.Close()
	paymentServer := httptest.NewServer(payment.Handler(payment.NewStore(payment.NewLedgerClient(ledgerServer.URL, time.Second)), logger))
	defer paymentServer.Close()

	request := map[string]any{"amount": 99, "currency": "USD"}
	var first, second payment.Payment
	doJSON(t, http.MethodPost, paymentServer.URL+"/v1/payments", "same-create", request, http.StatusCreated, &first)
	doJSON(t, http.MethodPost, paymentServer.URL+"/v1/payments", "same-create", request, http.StatusCreated, &second)
	if first.ID != second.ID {
		t.Fatalf("idempotent creates returned %q and %q", first.ID, second.ID)
	}
	var conflict errorEnvelope
	doJSON(t, http.MethodPost, paymentServer.URL+"/v1/payments", "same-create", map[string]any{"amount": 100, "currency": "USD"}, http.StatusConflict, &conflict)
	if conflict.Error.Code != "idempotency_conflict" {
		t.Fatalf("conflict = %+v", conflict)
	}

	body := bytes.NewBufferString(`{"amount":99,"currency":"USD","unknown":true}`)
	httpRequest, _ := http.NewRequest(http.MethodPost, paymentServer.URL+"/v1/payments", body)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Idempotency-Key", "unknown-field")
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var invalid errorEnvelope
	if err := json.NewDecoder(response.Body).Decode(&invalid); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest || invalid.Error.Code != "invalid_json" {
		t.Fatalf("unknown field status=%d body=%+v", response.StatusCode, invalid)
	}
}

func doJSON(t *testing.T, method, url, key string, input any, wantStatus int, output any) {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, url, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s status=%d want=%d body=%s", method, url, response.StatusCode, wantStatus, body)
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		t.Fatal(err)
	}
}

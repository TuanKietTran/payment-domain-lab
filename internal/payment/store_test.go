package payment

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"payment-sim/internal/httpx"
)

type fakeLedger struct {
	mu       sync.Mutex
	postings []LedgerPosting
	err      error
}

type ambiguousLedger struct {
	mu       sync.Mutex
	postings map[string]LedgerPosting
}

type contextLedger struct{}

func (contextLedger) Post(ctx context.Context, _ string, _ LedgerPosting) error {
	return ctx.Err()
}

func (l *ambiguousLedger) Post(_ context.Context, key string, posting LedgerPosting) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.postings[key]; ok {
		return nil
	}
	l.postings[key] = posting
	return httpx.Err(http.StatusBadGateway, "ledger_unavailable", "response was lost after commit")
}

func (f *fakeLedger) Post(_ context.Context, _ string, posting LedgerPosting) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.postings = append(f.postings, posting)
	return nil
}

func createTestPayment(t *testing.T, store *Store, amount int64) Payment {
	t.Helper()
	payment, err := store.Create("create", CreatePayment{Amount: amount, Currency: "usd", Description: " Test "})
	if err != nil {
		t.Fatal(err)
	}
	return payment
}

func TestLifecycleAndLedgerEntries(t *testing.T) {
	client := &fakeLedger{}
	store := NewStore(client)
	created := createTestPayment(t, store, 1000)
	if created.Status != StatusCreated || created.Currency != "USD" || created.Description != "Test" {
		t.Fatalf("unexpected created payment: %+v", created)
	}

	authorized, err := store.Act(context.Background(), created.ID, "authorize", "auth", Action{})
	if err != nil || authorized.Status != StatusAuthorized {
		t.Fatalf("authorize = %+v, %v", authorized, err)
	}
	captured, err := store.Act(context.Background(), created.ID, "capture", "capture", Action{})
	if err != nil || captured.Status != StatusCaptured {
		t.Fatalf("capture = %+v, %v", captured, err)
	}
	partial, err := store.Act(context.Background(), created.ID, "refund", "refund-1", Action{Amount: 300})
	if err != nil || partial.Status != StatusPartiallyRefunded || partial.RefundedAmount != 300 {
		t.Fatalf("partial refund = %+v, %v", partial, err)
	}
	refunded, err := store.Act(context.Background(), created.ID, "refund", "refund-2", Action{Amount: 700})
	if err != nil || refunded.Status != StatusRefunded || refunded.RefundedAmount != 1000 {
		t.Fatalf("full refund = %+v, %v", refunded, err)
	}

	want := [][]LedgerEntry{
		{{AccountID: "customer_payable_USD", Amount: -1000}, {AccountID: "merchant_pending_USD", Amount: 1000}},
		{{AccountID: "merchant_pending_USD", Amount: -1000}, {AccountID: "merchant_settled_USD", Amount: 1000}},
		{{AccountID: "merchant_settled_USD", Amount: -300}, {AccountID: "customer_payable_USD", Amount: 300}},
		{{AccountID: "merchant_settled_USD", Amount: -700}, {AccountID: "customer_payable_USD", Amount: 700}},
	}
	if len(client.postings) != len(want) {
		t.Fatalf("got %d postings, want %d", len(client.postings), len(want))
	}
	for i := range want {
		for j := range want[i] {
			if client.postings[i].Entries[j] != want[i][j] {
				t.Fatalf("posting %d entry %d = %+v, want %+v", i, j, client.postings[i].Entries[j], want[i][j])
			}
		}
	}
}

func TestCancelCreatedAndAuthorized(t *testing.T) {
	client := &fakeLedger{}
	store := NewStore(client)
	first := createTestPayment(t, store, 500)
	canceled, err := store.Act(context.Background(), first.ID, "cancel", "cancel-created", Action{})
	if err != nil || canceled.Status != StatusCanceled || len(client.postings) != 0 {
		t.Fatalf("cancel created = %+v, %v, postings %d", canceled, err, len(client.postings))
	}
	second, err := store.Create("create-2", CreatePayment{Amount: 500, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Act(context.Background(), second.ID, "authorize", "auth-2", Action{}); err != nil {
		t.Fatal(err)
	}
	canceled, err = store.Act(context.Background(), second.ID, "cancel", "cancel-auth", Action{})
	if err != nil || canceled.Status != StatusCanceled || len(client.postings) != 2 {
		t.Fatalf("cancel authorized = %+v, %v, postings %d", canceled, err, len(client.postings))
	}
}

func TestValidationIdempotencyAndAtomicFailure(t *testing.T) {
	client := &fakeLedger{}
	store := NewStore(client)
	payment := createTestPayment(t, store, 100)

	same, err := store.Create("create", CreatePayment{Amount: 100, Currency: "USD", Description: "Test"})
	if err != nil || same.ID != payment.ID {
		t.Fatalf("create retry = %+v, %v", same, err)
	}
	assertCode(t, func() error {
		_, err := store.Create("create", CreatePayment{Amount: 101, Currency: "USD"})
		return err
	}(), "idempotency_conflict")
	assertCode(t, func() error {
		_, err := store.Act(context.Background(), payment.ID, "capture", "bad-state", Action{})
		return err
	}(), "invalid_transition")
	assertCode(t, func() error {
		_, err := store.Act(context.Background(), payment.ID, "refund", "bad-refund", Action{Amount: 1})
		return err
	}(), "invalid_transition")

	client.err = errors.New("down")
	_, err = store.Act(context.Background(), payment.ID, "authorize", "auth", Action{})
	if err == nil {
		t.Fatal("expected downstream error")
	}
	unchanged, _ := store.Get(payment.ID)
	if unchanged.Status != StatusCreated {
		t.Fatalf("ledger failure changed status to %s", unchanged.Status)
	}
	client.err = nil
	authorized, err := store.Act(context.Background(), payment.ID, "authorize", "auth", Action{})
	if err != nil || authorized.Status != StatusAuthorized {
		t.Fatalf("retry after ledger recovery = %+v, %v", authorized, err)
	}
	retried, err := store.Act(context.Background(), payment.ID, "authorize", "auth", Action{})
	if err != nil || retried != authorized || len(client.postings) != 1 {
		t.Fatalf("idempotent action retry = %+v, %v; postings=%d", retried, err, len(client.postings))
	}
	assertCode(t, func() error {
		_, err := store.Act(context.Background(), payment.ID, "capture", "auth", Action{})
		return err
	}(), "idempotency_conflict")
}

func TestCanceledActionContextDoesNotMutatePayment(t *testing.T) {
	store := NewStore(contextLedger{})
	payment := createTestPayment(t, store, 250)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.Act(ctx, payment.ID, "authorize", "canceled-authorize", Action{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
	unchanged, _ := store.Get(payment.ID)
	if unchanged.Status != StatusCreated {
		t.Fatalf("canceled action mutated payment: %+v", unchanged)
	}
}

func TestAmbiguousLedgerCommitRecoversAtomicallyByIdempotencyKey(t *testing.T) {
	client := &ambiguousLedger{postings: make(map[string]LedgerPosting)}
	store := NewStore(client)
	payment := createTestPayment(t, store, 250)
	_, err := store.Act(context.Background(), payment.ID, "authorize", "ambiguous", Action{})
	assertCode(t, err, "ledger_unavailable")
	unchanged, _ := store.Get(payment.ID)
	if unchanged.Status != StatusCreated || len(client.postings) != 1 {
		t.Fatalf("first attempt payment=%+v postings=%d", unchanged, len(client.postings))
	}
	recovered, err := store.Act(context.Background(), payment.ID, "authorize", "ambiguous", Action{})
	if err != nil || recovered.Status != StatusAuthorized || len(client.postings) != 1 {
		t.Fatalf("retry payment=%+v error=%v postings=%d", recovered, err, len(client.postings))
	}
}

func TestConcurrentSameActionIsIdempotent(t *testing.T) {
	client := &fakeLedger{}
	store := NewStore(client)
	payment := createTestPayment(t, store, 250)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := store.Act(context.Background(), payment.ID, "authorize", "one-auth", Action{})
			if err == nil && result.Status != StatusAuthorized {
				err = errors.New("wrong status")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(client.postings) != 1 {
		t.Fatalf("concurrent retry created %d postings", len(client.postings))
	}
}

func TestRefundBoundsAndCreateValidation(t *testing.T) {
	store := NewStore(&fakeLedger{})
	for _, request := range []CreatePayment{{Amount: 0, Currency: "USD"}, {Amount: -1, Currency: "USD"}, {Amount: 1, Currency: "US1"}, {Amount: 1, Currency: "USD", Description: strings.Repeat("x", 201)}} {
		if _, err := store.Create("invalid", request); err == nil {
			t.Fatalf("expected error for %+v", request)
		}
	}
	assertCode(t, func() error {
		_, err := store.Create(strings.Repeat("k", 201), CreatePayment{Amount: 1, Currency: "USD"})
		return err
	}(), "invalid_idempotency_key")
	payment := createTestPayment(t, store, 100)
	_, _ = store.Act(context.Background(), payment.ID, "authorize", "a", Action{})
	_, _ = store.Act(context.Background(), payment.ID, "capture", "c", Action{})
	assertCode(t, func() error {
		_, err := store.Act(context.Background(), payment.ID, "refund", "r0", Action{})
		return err
	}(), "invalid_amount")
	assertCode(t, func() error {
		_, err := store.Act(context.Background(), payment.ID, "refund", "r101", Action{Amount: 101})
		return err
	}(), "refund_exceeds_remaining")
	current, _ := store.Get(payment.ID)
	if current.RefundedAmount != 0 || current.Status != StatusCaptured {
		t.Fatalf("invalid refund mutated payment: %+v", current)
	}
	partial, err := store.Act(context.Background(), payment.ID, "refund", "r60", Action{Amount: 60})
	if err != nil || partial.RefundedAmount != 60 || partial.Status != StatusPartiallyRefunded {
		t.Fatalf("partial refund = %+v, %v", partial, err)
	}
	assertCode(t, func() error {
		_, err := store.Act(context.Background(), payment.ID, "refund", "r41-after-60", Action{Amount: 41})
		return err
	}(), "refund_exceeds_remaining")
	current, _ = store.Get(payment.ID)
	if current != partial {
		t.Fatalf("cumulative over-refund mutated payment: before=%+v after=%+v", partial, current)
	}
	final, err := store.Act(context.Background(), payment.ID, "refund", "r40", Action{Amount: 40})
	if err != nil || final.RefundedAmount != 100 || final.Status != StatusRefunded {
		t.Fatalf("final cumulative refund = %+v, %v", final, err)
	}
}

func TestConcurrentCreateIdempotencyAndPayloadConflict(t *testing.T) {
	t.Run("same payload", func(t *testing.T) {
		store := NewStore(&fakeLedger{})
		const workers = 32
		results := make(chan Payment, workers)
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				result, err := store.Create("same-create", CreatePayment{Amount: 75, Currency: " usd ", Description: " item "})
				results <- result
				errs <- err
			}()
		}
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		var id string
		for result := range results {
			if id == "" {
				id = result.ID
			}
			if result.ID != id {
				t.Fatalf("duplicate key returned IDs %q and %q", id, result.ID)
			}
		}
		if got := len(store.List()); got != 1 {
			t.Fatalf("payments=%d, want 1", got)
		}
	})

	t.Run("different payload", func(t *testing.T) {
		store := NewStore(&fakeLedger{})
		type outcome struct {
			amount int64
			item   Payment
			err    error
		}
		const perPayload = 16
		outcomes := make(chan outcome, perPayload*2)
		var wg sync.WaitGroup
		for _, amount := range []int64{100, 101} {
			for i := 0; i < perPayload; i++ {
				wg.Add(1)
				go func(amount int64) {
					defer wg.Done()
					item, err := store.Create("contended-create", CreatePayment{Amount: amount, Currency: "USD"})
					outcomes <- outcome{amount: amount, item: item, err: err}
				}(amount)
			}
		}
		wg.Wait()
		close(outcomes)
		var winningAmount int64
		successes, conflicts := 0, 0
		for outcome := range outcomes {
			if outcome.err == nil {
				successes++
				if winningAmount == 0 {
					winningAmount = outcome.amount
				}
				if outcome.amount != winningAmount || outcome.item.Amount != winningAmount {
					t.Fatalf("inconsistent winner: amount=%d payment=%+v winner=%d", outcome.amount, outcome.item, winningAmount)
				}
				continue
			}
			var api *httpx.APIError
			if !errors.As(outcome.err, &api) || api.Code != "idempotency_conflict" {
				t.Fatalf("unexpected error: %v", outcome.err)
			}
			conflicts++
		}
		if successes != perPayload || conflicts != perPayload || len(store.List()) != 1 {
			t.Fatalf("successes=%d conflicts=%d payments=%d", successes, conflicts, len(store.List()))
		}
	})
}

func TestPaymentAmountBoundaryAndCumulativeRefunds(t *testing.T) {
	client := &fakeLedger{}
	store := NewStore(client)
	payment := createTestPayment(t, store, math.MaxInt64)
	if _, err := store.Act(context.Background(), payment.ID, "authorize", "max-authorize", Action{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Act(context.Background(), payment.ID, "capture", "max-capture", Action{}); err != nil {
		t.Fatal(err)
	}
	refunds := []int64{1, math.MaxInt64 - 2, 1}
	wantTotals := []int64{1, math.MaxInt64 - 1, math.MaxInt64}
	for i, amount := range refunds {
		result, err := store.Act(context.Background(), payment.ID, "refund", "max-refund-"+string(rune('a'+i)), Action{Amount: amount})
		if err != nil {
			t.Fatal(err)
		}
		if result.RefundedAmount != wantTotals[i] {
			t.Fatalf("refund %d cumulative=%d want=%d", i, result.RefundedAmount, wantTotals[i])
		}
		wantStatus := StatusPartiallyRefunded
		if i == len(refunds)-1 {
			wantStatus = StatusRefunded
		}
		if result.Status != wantStatus {
			t.Fatalf("refund %d status=%s want=%s", i, result.Status, wantStatus)
		}
	}
	if len(client.postings) != 5 {
		t.Fatalf("postings=%d, want 5", len(client.postings))
	}
}

func TestIllegalStateTransitionsDoNotMutate(t *testing.T) {
	tests := []struct {
		name       string
		setup      []string
		operation  string
		request    Action
		wantStatus Status
	}{
		{"capture created", nil, "capture", Action{}, StatusCreated},
		{"refund created", nil, "refund", Action{Amount: 1}, StatusCreated},
		{"authorize authorized", []string{"authorize"}, "authorize", Action{}, StatusAuthorized},
		{"refund authorized", []string{"authorize"}, "refund", Action{Amount: 1}, StatusAuthorized},
		{"cancel captured", []string{"authorize", "capture"}, "cancel", Action{}, StatusCaptured},
		{"capture captured", []string{"authorize", "capture"}, "capture", Action{}, StatusCaptured},
		{"cancel partially refunded", []string{"authorize", "capture", "refund"}, "cancel", Action{}, StatusPartiallyRefunded},
		{"refund refunded", []string{"authorize", "capture", "refund-full"}, "refund", Action{Amount: 1}, StatusRefunded},
		{"authorize canceled", []string{"cancel"}, "authorize", Action{}, StatusCanceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeLedger{}
			store := NewStore(client)
			payment := createTestPayment(t, store, 10)
			for i, operation := range test.setup {
				request := Action{}
				if operation == "refund" {
					request.Amount = 4
				}
				if operation == "refund-full" {
					operation, request.Amount = "refund", 10
				}
				if _, err := store.Act(context.Background(), payment.ID, operation, "setup-"+string(rune('a'+i)), request); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := store.Get(payment.ID)
			postings := len(client.postings)
			_, err := store.Act(context.Background(), payment.ID, test.operation, "illegal", test.request)
			assertCode(t, err, "invalid_transition")
			after, _ := store.Get(payment.ID)
			if after != before || after.Status != test.wantStatus || len(client.postings) != postings {
				t.Fatalf("illegal transition mutated state: before=%+v after=%+v postings=%d want=%d", before, after, len(client.postings), postings)
			}
		})
	}
}

func TestRandomizedLifecycleConservesPaymentAndLedgerAmounts(t *testing.T) {
	seed := int64(20241010)
	rng := rand.New(rand.NewSource(seed))
	for scenario := 0; scenario < 100; scenario++ {
		client := &fakeLedger{}
		store := NewStore(client)
		amount := int64(rng.Intn(10_000) + 1)
		payment := createTestPayment(t, store, amount)
		expectedStatus := StatusCreated
		expectedRefunded := int64(0)
		authorized := false
		captured := false
		canceled := false
		for step := 0; step < 50; step++ {
			operation := []string{"authorize", "capture", "cancel", "refund"}[rng.Intn(4)]
			request := Action{}
			if operation == "refund" {
				remaining := amount - expectedRefunded
				if remaining > 0 && rng.Intn(3) != 0 {
					request.Amount = int64(rng.Intn(int(min64(remaining, 1000))) + 1)
				} else {
					request.Amount = remaining + 1
				}
			}
			before, _ := store.Get(payment.ID)
			postingsBefore := len(client.postings)
			result, err := store.Act(context.Background(), payment.ID, operation, "scenario-"+strconv.Itoa(scenario)+"-step-"+strconv.Itoa(step), request)
			valid := false
			switch operation {
			case "authorize":
				valid = expectedStatus == StatusCreated
				if valid {
					expectedStatus, authorized = StatusAuthorized, true
				}
			case "capture":
				valid = expectedStatus == StatusAuthorized
				if valid {
					expectedStatus, captured = StatusCaptured, true
				}
			case "cancel":
				valid = expectedStatus == StatusCreated || expectedStatus == StatusAuthorized
				if valid {
					expectedStatus, canceled = StatusCanceled, true
				}
			case "refund":
				valid = (expectedStatus == StatusCaptured || expectedStatus == StatusPartiallyRefunded) && request.Amount > 0 && request.Amount <= amount-expectedRefunded
				if valid {
					expectedRefunded += request.Amount
					if expectedRefunded == amount {
						expectedStatus = StatusRefunded
					} else {
						expectedStatus = StatusPartiallyRefunded
					}
				}
			}
			if valid {
				if err != nil || result.Status != expectedStatus || result.RefundedAmount != expectedRefunded {
					t.Fatalf("seed=%d scenario=%d step=%d operation=%s result=%+v err=%v want status=%s refunded=%d", seed, scenario, step, operation, result, err, expectedStatus, expectedRefunded)
				}
				continue
			}
			if err == nil {
				t.Fatalf("seed=%d scenario=%d step=%d invalid operation %s succeeded", seed, scenario, step, operation)
			}
			after, _ := store.Get(payment.ID)
			if after != before || len(client.postings) != postingsBefore {
				t.Fatalf("seed=%d scenario=%d step=%d invalid operation mutated before=%+v after=%+v postings %d->%d", seed, scenario, step, before, after, postingsBefore, len(client.postings))
			}
			if canceled || expectedStatus == StatusRefunded {
				break
			}
		}
		var customer, pending, settled int64
		for _, posting := range client.postings {
			for _, entry := range posting.Entries {
				switch entry.AccountID {
				case customerAccount("USD"):
					customer += entry.Amount
				case pendingAccount("USD"):
					pending += entry.Amount
				case settledAccount("USD"):
					settled += entry.Amount
				}
			}
		}
		wantCustomer, wantPending, wantSettled := int64(0), int64(0), int64(0)
		if authorized || captured || expectedRefunded > 0 {
			wantCustomer -= amount
			wantPending += amount
		}
		if captured || expectedRefunded > 0 {
			wantPending -= amount
			wantSettled += amount
		}
		if canceled && authorized && !captured {
			wantPending -= amount
			wantCustomer += amount
		}
		wantSettled -= expectedRefunded
		wantCustomer += expectedRefunded
		if customer != wantCustomer || pending != wantPending || settled != wantSettled || customer+pending+settled != 0 {
			t.Fatalf("seed=%d scenario=%d ledger customer=%d pending=%d settled=%d want %d/%d/%d", seed, scenario, customer, pending, settled, wantCustomer, wantPending, wantSettled)
		}
	}
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var api *httpx.APIError
	if !errors.As(err, &api) || api.Code != code {
		t.Fatalf("error = %v, want API code %s", err, code)
	}
	if api.Status < http.StatusBadRequest {
		t.Fatalf("error status = %d", api.Status)
	}
}

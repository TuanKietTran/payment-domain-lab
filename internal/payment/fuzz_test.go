package payment

import (
	"context"
	"testing"
)

// setupState prepares a payment in one of four reachable states:
//
//	0 = created (no transitions)
//	1 = authorized
//	2 = captured
//	3 = partially_refunded (refunded 1 of 100)
//
// Returns the payment and number of postings already committed.
// Uses fixed idempotency keys so setup itself is deterministic and never
// conflicts with the "fuzz-act" key used by the actual fuzz step.
func setupState(t *testing.T, state uint8) (*Store, Payment, int, *fakeLedger) {
	t.Helper()
	client := &fakeLedger{}
	store := NewStore(client)
	p, err := store.Create("fuzz-create", CreatePayment{Amount: 100, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	switch state % 4 {
	case 1:
		if _, err := store.Act(ctx, p.ID, "authorize", "fuzz-setup-1", Action{}); err != nil {
			t.Fatal(err)
		}
	case 2:
		if _, err := store.Act(ctx, p.ID, "authorize", "fuzz-setup-1", Action{}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Act(ctx, p.ID, "capture", "fuzz-setup-2", Action{}); err != nil {
			t.Fatal(err)
		}
	case 3:
		if _, err := store.Act(ctx, p.ID, "authorize", "fuzz-setup-1", Action{}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Act(ctx, p.ID, "capture", "fuzz-setup-2", Action{}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Act(ctx, p.ID, "refund", "fuzz-setup-3", Action{Amount: 1}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := store.Get(p.ID)
	return store, got, len(client.postings), client
}

func FuzzAct(f *testing.F) {
	// Seeds: (setupState, action, amount)
	// setupState selects which of the four reachable states to prepare.
	type seed struct {
		setup  uint8
		action string
		amount int64
	}
	seeds := []seed{
		// from created
		{0, "authorize", 0},
		{0, "cancel", 0},
		{0, "capture", 0}, // invalid — must not mutate
		{0, "refund", 1},  // invalid — must not mutate
		{0, "unknown", 0}, // invalid — must not mutate
		// from authorized
		{1, "capture", 0},   // valid
		{1, "cancel", 0},    // valid
		{1, "authorize", 0}, // invalid
		{1, "refund", 1},    // invalid
		// from captured
		{2, "refund", 1},   // partial refund — valid
		{2, "refund", 100}, // full refund — valid, transitions to refunded
		{2, "refund", 0},   // invalid amount
		{2, "refund", -1},  // invalid amount
		{2, "refund", 101}, // exceeds amount — invalid
		{2, "capture", 0},  // invalid
		{2, "cancel", 0},   // invalid
		// from partially_refunded (1 of 100 already refunded)
		{3, "refund", 1},   // another partial — valid
		{3, "refund", 99},  // completes refund — valid
		{3, "refund", 100}, // exceeds remaining — invalid
		{3, "capture", 0},  // invalid
		// boundary amounts
		{2, "refund", 9223372036854775807},
		{0, "authorize", 9223372036854775807},
	}
	for _, s := range seeds {
		f.Add(s.setup, s.action, s.amount)
	}

	f.Fuzz(func(t *testing.T, setup uint8, action string, amount int64) {
		store, before, postingsBefore, client := setupState(t, setup)

		result, err := store.Act(context.Background(), before.ID, action, "fuzz-act", Action{Amount: amount})
		if err != nil {
			// On failure: payment and posting count must be unchanged.
			after, _ := store.Get(before.ID)
			if after != before {
				t.Fatalf("failed action %q amount=%d setup=%d mutated payment: before=%+v after=%+v",
					action, amount, setup%4, before, after)
			}
			if len(client.postings) != postingsBefore {
				t.Fatalf("failed action %q amount=%d setup=%d changed postings %d->%d",
					action, amount, setup%4, postingsBefore, len(client.postings))
			}
			return
		}
		// On success: status must be a valid enum value.
		validStatuses := map[Status]bool{
			StatusCreated: true, StatusAuthorized: true, StatusCaptured: true,
			StatusPartiallyRefunded: true, StatusRefunded: true, StatusCanceled: true,
		}
		if !validStatuses[result.Status] {
			t.Fatalf("action %q setup=%d produced invalid status %q", action, setup%4, result.Status)
		}
		// refunded_amount must be in [0, payment.Amount].
		if result.RefundedAmount < 0 || result.RefundedAmount > result.Amount {
			t.Fatalf("action %q setup=%d produced invalid refunded_amount=%d amount=%d",
				action, setup%4, result.RefundedAmount, result.Amount)
		}
		// If status is refunded, refunded_amount must equal amount.
		if result.Status == StatusRefunded && result.RefundedAmount != result.Amount {
			t.Fatalf("status=refunded but refunded_amount=%d != amount=%d", result.RefundedAmount, result.Amount)
		}
		// If status is partially_refunded, refunded_amount must be in (0, amount).
		if result.Status == StatusPartiallyRefunded && (result.RefundedAmount <= 0 || result.RefundedAmount >= result.Amount) {
			t.Fatalf("status=partially_refunded but refunded_amount=%d amount=%d", result.RefundedAmount, result.Amount)
		}
	})
}

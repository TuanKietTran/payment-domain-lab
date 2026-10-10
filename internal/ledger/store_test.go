package ledger

import (
	"errors"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"testing"

	"payment-sim/internal/httpx"
)

func TestBalancedPostingAndIdempotency(t *testing.T) {
	s := NewStore()
	for _, r := range []CreateAccount{{ID: "a", Name: "A", Currency: "usd"}, {ID: "b", Name: "B", Currency: "USD"}} {
		if _, err := s.CreateAccount("create-"+r.ID, r); err != nil {
			t.Fatal(err)
		}
	}
	req := CreatePosting{Currency: "USD", Description: "test", Entries: []Entry{{AccountID: "a", Amount: -125}, {AccountID: "b", Amount: 125}}}
	p, err := s.CreatePosting("key", req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CreatePosting("key", req)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != again.ID {
		t.Fatal("idempotency returned a different posting")
	}
	a, _ := s.Account("a")
	b, _ := s.Account("b")
	if a.Balance != -125 || b.Balance != 125 {
		t.Fatalf("balances = %d, %d", a.Balance, b.Balance)
	}
}
func TestPostingValidationIsAtomic(t *testing.T) {
	s := NewStore()
	s.CreateAccount("a", CreateAccount{ID: "a", Name: "A", Currency: "USD"})
	s.CreateAccount("b", CreateAccount{ID: "b", Name: "B", Currency: "USD"})
	if _, err := s.CreatePosting("bad", CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: "a", Amount: -2}, {AccountID: "b", Amount: 1}}}); err == nil {
		t.Fatal("expected unbalanced error")
	}
	a, _ := s.Account("a")
	if a.Balance != 0 || len(s.Postings()) != 0 {
		t.Fatal("invalid posting mutated store")
	}
}
func TestIdempotencyConflict(t *testing.T) {
	s := NewStore()
	s.CreateAccount("key", CreateAccount{ID: "a", Name: "A", Currency: "USD"})
	if _, err := s.CreateAccount("key", CreateAccount{ID: "b", Name: "B", Currency: "USD"}); err == nil {
		t.Fatal("expected conflict")
	}
}

func TestIdempotencyKeyCannotCrossOperations(t *testing.T) {
	s := NewStore()
	if _, err := s.CreateAccount("shared", CreateAccount{ID: "a", Name: "A", Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePosting("shared", CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: "a", Amount: -1}, {AccountID: "a", Amount: 1}}}); err == nil {
		t.Fatal("expected cross-operation idempotency conflict")
	}
}

func TestPostingResultsAreImmutableCopies(t *testing.T) {
	s := NewStore()
	_, _ = s.CreateAccount("a", CreateAccount{ID: "a", Name: "A", Currency: "USD"})
	_, _ = s.CreateAccount("b", CreateAccount{ID: "b", Name: "B", Currency: "USD"})
	request := CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: "a", Amount: -10}, {AccountID: "b", Amount: 10}}}
	posting, err := s.CreatePosting("posting", request)
	if err != nil {
		t.Fatal(err)
	}
	posting.Entries[0].Amount = 999
	request.Entries[0].Amount = 888
	listed := s.Postings()
	if listed[0].Entries[0].Amount != -10 {
		t.Fatalf("stored posting was mutated: %+v", listed[0])
	}
	listed[0].Entries[0].Amount = 777
	retried, err := s.CreatePosting("posting", CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: "a", Amount: -10}, {AccountID: "b", Amount: 10}}})
	if err != nil || retried.Entries[0].Amount != -10 {
		t.Fatalf("idempotent result was mutated: %+v, %v", retried, err)
	}
}

func TestBalanceOverflowIsAtomic(t *testing.T) {
	s := NewStore()
	_, _ = s.CreateAccount("a", CreateAccount{ID: "a", Name: "A", Currency: "USD"})
	_, _ = s.CreateAccount("b", CreateAccount{ID: "b", Name: "B", Currency: "USD"})
	_, err := s.CreatePosting("max", CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: "a", Amount: -math.MaxInt64}, {AccountID: "b", Amount: math.MaxInt64}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreatePosting("overflow", CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: "a", Amount: -1}, {AccountID: "b", Amount: 1}}}); err == nil {
		t.Fatal("expected balance overflow")
	}
	a, _ := s.Account("a")
	b, _ := s.Account("b")
	if a.Balance != -math.MaxInt64 || b.Balance != math.MaxInt64 || len(s.Postings()) != 1 {
		t.Fatalf("overflow changed state: a=%d b=%d postings=%d", a.Balance, b.Balance, len(s.Postings()))
	}
}

func TestInputBounds(t *testing.T) {
	s := NewStore()
	if _, err := s.CreateAccount(strings.Repeat("k", 201), CreateAccount{Name: "A", Currency: "USD"}); err == nil {
		t.Fatal("expected idempotency key length error")
	}
	if _, err := s.CreateAccount("long-name", CreateAccount{Name: strings.Repeat("n", 201), Currency: "USD"}); err == nil {
		t.Fatal("expected account name length error")
	}
	_, _ = s.CreateAccount("a", CreateAccount{ID: "a", Name: "A", Currency: "USD"})
	if _, err := s.CreatePosting("long-description", CreatePosting{Currency: "USD", Description: strings.Repeat("d", 501), Entries: []Entry{{AccountID: "a", Amount: -1}, {AccountID: "a", Amount: 1}}}); err == nil {
		t.Fatal("expected posting description length error")
	}
}

func TestConcurrentAccountRetryMutatesOnce(t *testing.T) {
	s := NewStore()
	const workers = 32
	results := make(chan Account, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			account, err := s.CreateAccount("same-account", CreateAccount{ID: "a", Name: " A ", Currency: " usd "})
			results <- account
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
	for account := range results {
		if account.ID != "a" || account.Name != "A" || account.Currency != "USD" {
			t.Fatalf("unexpected account: %+v", account)
		}
	}
	if got := len(s.Accounts()); got != 1 {
		t.Fatalf("accounts=%d, want 1", got)
	}
}

func TestPostingCanonicalizesEntryAccountIDsWithoutMutatingInput(t *testing.T) {
	s := NewStore()
	_, _ = s.CreateAccount("a", CreateAccount{ID: "a", Name: "A", Currency: "USD"})
	_, _ = s.CreateAccount("b", CreateAccount{ID: "b", Name: "B", Currency: "USD"})
	entries := []Entry{{AccountID: " a ", Amount: -5}, {AccountID: " b ", Amount: 5}}
	posting, err := s.CreatePosting("canonical-posting", CreatePosting{Currency: " usd ", Description: " transfer ", Entries: entries})
	if err != nil {
		t.Fatalf("canonical posting rejected: %v", err)
	}
	if entries[0].AccountID != " a " || posting.Entries[0].AccountID != "a" || posting.Entries[1].AccountID != "b" || posting.Description != "transfer" {
		t.Fatalf("input=%+v posting=%+v", entries, posting)
	}
	retried, err := s.CreatePosting("canonical-posting", CreatePosting{Currency: "USD", Description: "transfer", Entries: []Entry{{AccountID: "a", Amount: -5}, {AccountID: "b", Amount: 5}}})
	if err != nil || retried.ID != posting.ID || len(s.Postings()) != 1 {
		t.Fatalf("canonical retry=%+v error=%v postings=%d", retried, err, len(s.Postings()))
	}
}

func TestPostingIdempotencyPayloadConflictIsAtomic(t *testing.T) {
	s := NewStore()
	_, _ = s.CreateAccount("a", CreateAccount{ID: "a", Name: "A", Currency: "USD"})
	_, _ = s.CreateAccount("b", CreateAccount{ID: "b", Name: "B", Currency: "USD"})
	first := CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: "a", Amount: -5}, {AccountID: "b", Amount: 5}}}
	if _, err := s.CreatePosting("same-posting", first); err != nil {
		t.Fatal(err)
	}
	second := CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: "a", Amount: -6}, {AccountID: "b", Amount: 6}}}
	ledgerAssertCode(t, func() error {
		_, err := s.CreatePosting("same-posting", second)
		return err
	}(), "idempotency_conflict")
	a, _ := s.Account("a")
	b, _ := s.Account("b")
	if a.Balance != -5 || b.Balance != 5 || len(s.Postings()) != 1 {
		t.Fatalf("conflict mutated state: a=%d b=%d postings=%d", a.Balance, b.Balance, len(s.Postings()))
	}
}

func TestPostingAccountCurrencyAndBalanceInvariantsAreAtomic(t *testing.T) {
	tests := []struct {
		name string
		want string
		req  CreatePosting
	}{
		{"unbalanced", "unbalanced_posting", CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: "a", Amount: -2}, {AccountID: "b", Amount: 1}}}},
		{"zero entry", "invalid_entry", CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: "a", Amount: 0}, {AccountID: "b", Amount: 1}}}},
		{"missing account", "account_not_found", CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: "a", Amount: -1}, {AccountID: "missing", Amount: 1}}}},
		{"currency mismatch", "currency_mismatch", CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: "a", Amount: -1}, {AccountID: "eur", Amount: 1}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := NewStore()
			_, _ = s.CreateAccount("a", CreateAccount{ID: "a", Name: "A", Currency: "USD"})
			_, _ = s.CreateAccount("b", CreateAccount{ID: "b", Name: "B", Currency: "USD"})
			_, _ = s.CreateAccount("eur", CreateAccount{ID: "eur", Name: "EUR", Currency: "EUR"})
			ledgerAssertCode(t, func() error {
				_, err := s.CreatePosting("invalid", test.req)
				return err
			}(), test.want)
			for _, id := range []string{"a", "b", "eur"} {
				account, _ := s.Account(id)
				if account.Balance != 0 {
					t.Fatalf("account %s balance=%d", id, account.Balance)
				}
			}
			if len(s.Postings()) != 0 {
				t.Fatalf("invalid posting was retained")
			}
		})
	}
}

func TestRandomizedExactBalancedPostingsConserveLedger(t *testing.T) {
	seed := int64(20241011)
	rng := rand.New(rand.NewSource(seed))
	for scenario := 0; scenario < 100; scenario++ {
		s := NewStore()
		for _, id := range []string{"a", "b", "c", "d"} {
			_, _ = s.CreateAccount("acct-"+id, CreateAccount{ID: id, Name: id, Currency: "USD"})
		}
		expected := map[string]int64{"a": 0, "b": 0, "c": 0, "d": 0}
		for step := 0; step < 30; step++ {
			from, to := []string{"a", "b", "c", "d"}[rng.Intn(4)], []string{"a", "b", "c", "d"}[rng.Intn(4)]
			if from == to {
				continue
			}
			amount := int64(rng.Intn(10_000) + 1)
			beforePostings := len(s.Postings())
			posting, err := s.CreatePosting("scenario-"+strconv.Itoa(scenario)+"-step-"+strconv.Itoa(step), CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: from, Amount: -amount}, {AccountID: to, Amount: amount}}})
			if err != nil {
				t.Fatalf("seed=%d scenario=%d step=%d: %v", seed, scenario, step, err)
			}
			expected[from] -= amount
			expected[to] += amount
			var sum int64
			for _, entry := range posting.Entries {
				sum += entry.Amount
			}
			if sum != 0 || len(s.Postings()) != beforePostings+1 {
				t.Fatalf("seed=%d scenario=%d step=%d posting=%+v postings=%d", seed, scenario, step, posting, len(s.Postings()))
			}
		}
		var total int64
		for id, want := range expected {
			account, _ := s.Account(id)
			if account.Balance != want {
				t.Fatalf("seed=%d scenario=%d account %s balance=%d want=%d", seed, scenario, id, account.Balance, want)
			}
			total += account.Balance
		}
		if total != 0 {
			t.Fatalf("seed=%d scenario=%d total balance=%d", seed, scenario, total)
		}
	}
}

func TestUnicodeStringBoundsAreByteBounds(t *testing.T) {
	s := NewStore()
	if _, err := s.CreateAccount("unicode-name", CreateAccount{ID: "unicode", Name: strings.Repeat("😀", 50), Currency: "USD"}); err != nil {
		t.Fatalf("200-byte name rejected: %v", err)
	}
	if _, err := s.CreateAccount("too-long-unicode-name", CreateAccount{ID: "unicode2", Name: strings.Repeat("😀", 51), Currency: "USD"}); err == nil {
		t.Fatal("expected 204-byte name to be rejected")
	}
	if _, err := s.CreatePosting("unicode-description", CreatePosting{Currency: "USD", Description: strings.Repeat("日", 166), Entries: []Entry{{AccountID: "unicode", Amount: -1}, {AccountID: "unicode", Amount: 1}}}); err != nil {
		t.Fatalf("498-byte description rejected: %v", err)
	}
	if _, err := s.CreatePosting("too-long-unicode-description", CreatePosting{Currency: "USD", Description: strings.Repeat("日", 167), Entries: []Entry{{AccountID: "unicode", Amount: -1}, {AccountID: "unicode", Amount: 1}}}); err == nil {
		t.Fatal("expected 501-byte description to be rejected")
	}
}

func TestPerAccountDeltaUsesExactArithmetic(t *testing.T) {
	s := NewStore()
	_, _ = s.CreateAccount("a", CreateAccount{ID: "a", Name: "A", Currency: "USD"})
	_, _ = s.CreateAccount("b", CreateAccount{ID: "b", Name: "B", Currency: "USD"})
	request := CreatePosting{Currency: "USD", Entries: []Entry{
		{AccountID: "a", Amount: math.MaxInt64},
		{AccountID: "a", Amount: 1},
		{AccountID: "a", Amount: math.MinInt64},
	}}
	posting, err := s.CreatePosting("exact-delta", request)
	if err != nil {
		t.Fatalf("mathematically zero per-account delta rejected: %v", err)
	}
	account, _ := s.Account("a")
	if account.Balance != 0 || len(posting.Entries) != 3 || len(s.Postings()) != 1 {
		t.Fatalf("account=%+v posting=%+v retained=%d", account, posting, len(s.Postings()))
	}
	ledgerAssertCode(t, func() error {
		_, err := s.CreatePosting("out-of-range-delta", CreatePosting{Currency: "USD", Entries: []Entry{
			{AccountID: "a", Amount: math.MaxInt64},
			{AccountID: "a", Amount: 1},
			{AccountID: "b", Amount: math.MinInt64},
		}})
		return err
	}(), "amount_overflow")
	afterA, _ := s.Account("a")
	afterB, _ := s.Account("b")
	if afterA.Balance != 0 || afterB.Balance != 0 || len(s.Postings()) != 1 {
		t.Fatalf("out-of-range delta mutated state: a=%d b=%d postings=%d", afterA.Balance, afterB.Balance, len(s.Postings()))
	}
}

func TestBalancedPostingSupportsFullInt64Boundaries(t *testing.T) {
	s := NewStore()
	for _, id := range []string{"min", "max", "one"} {
		_, _ = s.CreateAccount("create-"+id, CreateAccount{ID: id, Name: id, Currency: "USD"})
	}
	// In this order the running int64 sum would overflow at MaxInt64 + 1,
	// although the exact mathematical sum is zero.
	boundary := CreatePosting{Currency: "USD", Entries: []Entry{
		{AccountID: "max", Amount: math.MaxInt64},
		{AccountID: "one", Amount: 1},
		{AccountID: "min", Amount: math.MinInt64},
	}}
	if _, err := s.CreatePosting("boundary", boundary); err != nil {
		t.Fatalf("balanced boundary posting rejected: %v", err)
	}
	min, _ := s.Account("min")
	max, _ := s.Account("max")
	one, _ := s.Account("one")
	if min.Balance != math.MinInt64 || max.Balance != math.MaxInt64 || one.Balance != 1 {
		t.Fatalf("boundary balances: min=%d max=%d one=%d", min.Balance, max.Balance, one.Balance)
	}
	ledgerAssertCode(t, func() error {
		_, err := s.CreatePosting("overflow", CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: "min", Amount: -1}, {AccountID: "max", Amount: 1}}})
		return err
	}(), "balance_overflow")
	minAfter, _ := s.Account("min")
	maxAfter, _ := s.Account("max")
	if minAfter != min || maxAfter != max || len(s.Postings()) != 1 {
		t.Fatalf("overflow mutated state: min=%+v max=%+v postings=%d", minAfter, maxAfter, len(s.Postings()))
	}
}

func ledgerAssertCode(t *testing.T, err error, code string) {
	t.Helper()
	var api *httpx.APIError
	if !errors.As(err, &api) || api.Code != code {
		t.Fatalf("error=%v, want API code %s", err, code)
	}
}

func TestConcurrentSnapshotsDoNotAliasStoredPostings(t *testing.T) {
	s := NewStore()
	_, _ = s.CreateAccount("a", CreateAccount{ID: "a", Name: "A", Currency: "USD"})
	_, _ = s.CreateAccount("b", CreateAccount{ID: "b", Name: "B", Currency: "USD"})
	const writes = 100
	start := make(chan struct{})
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < writes; i++ {
			if _, err := s.CreatePosting("snapshot-"+strconv.Itoa(i), CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: "a", Amount: -1}, {AccountID: "b", Amount: 1}}}); err != nil {
				errs <- err
				return
			}
		}
	}()
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < writes; i++ {
				for _, posting := range s.Postings() {
					posting.Entries[0].Amount = 999
				}
				_ = s.Accounts()
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	postings := s.Postings()
	if len(postings) != writes {
		t.Fatalf("postings=%d, want %d", len(postings), writes)
	}
	for i, posting := range postings {
		if posting.Entries[0].Amount != -1 || posting.Entries[1].Amount != 1 {
			t.Fatalf("posting %d was aliased: %+v", i, posting)
		}
	}
}

func TestConcurrentPostingRetryMutatesOnce(t *testing.T) {
	s := NewStore()
	_, _ = s.CreateAccount("a", CreateAccount{ID: "a", Name: "A", Currency: "USD"})
	_, _ = s.CreateAccount("b", CreateAccount{ID: "b", Name: "B", Currency: "USD"})
	request := CreatePosting{Currency: "USD", Entries: []Entry{{AccountID: "a", Amount: -5}, {AccountID: "b", Amount: 5}}}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.CreatePosting("same", request); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(s.Postings()) != 1 {
		t.Fatalf("got %d postings", len(s.Postings()))
	}
}

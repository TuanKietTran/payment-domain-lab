package ledger

import (
	"math"
	"testing"
)

func FuzzCreatePosting(f *testing.F) {
	type seed struct {
		currency    string
		description string
		acct1       string
		amount1     int64
		acct2       string
		amount2     int64
	}
	seeds := []seed{
		{"USD", "transfer", "a", -5, "b", 5},
		{"USD", "", "a", -1, "b", 1},
		{"usd", "  spaces  ", " a ", -100, " b ", 100},
		{"EUR", "cross", "a", -1, "b", 1},
		{"USD", "unbalanced", "a", -2, "b", 1},
		{"USD", "zero entry", "a", 0, "b", 1},
		{"USD", "max", "a", -math.MaxInt64, "b", math.MaxInt64},
		{"USD", "min overflow", "a", math.MinInt64, "b", math.MaxInt64},
		{"USD", "same account", "a", -1, "a", 1},
		{"", "no currency", "a", -1, "b", 1},
		{"TOOLONG", "bad currency", "a", -1, "b", 1},
	}
	for _, s := range seeds {
		f.Add(s.currency, s.description, s.acct1, s.amount1, s.acct2, s.amount2)
	}
	f.Fuzz(func(t *testing.T, currency, description, acct1 string, amount1 int64, acct2 string, amount2 int64) {
		s := NewStore()
		_, _ = s.CreateAccount("a", CreateAccount{ID: "a", Name: "A", Currency: "USD"})
		_, _ = s.CreateAccount("b", CreateAccount{ID: "b", Name: "B", Currency: "USD"})

		req := CreatePosting{
			Currency:    currency,
			Description: description,
			Entries:     []Entry{{AccountID: acct1, Amount: amount1}, {AccountID: acct2, Amount: amount2}},
		}
		posting, err := s.CreatePosting("fuzz-key", req)
		if err != nil {
			// state must be unchanged
			if len(s.Postings()) != 0 {
				t.Fatalf("failed posting left %d postings", len(s.Postings()))
			}
			acctA, _ := s.Account("a")
			acctB, _ := s.Account("b")
			if acctA.Balance != 0 || acctB.Balance != 0 {
				t.Fatalf("failed posting mutated balances: a=%d b=%d", acctA.Balance, acctB.Balance)
			}
			return
		}
		// balance must be zero
		var sum int64
		for _, e := range posting.Entries {
			sum += e.Amount
		}
		if sum != 0 {
			t.Fatalf("posting not balanced: sum=%d entries=%+v", sum, posting.Entries)
		}
		// account balances must also sum to zero
		var total int64
		for _, acct := range s.Accounts() {
			total += acct.Balance
		}
		if total != 0 {
			t.Fatalf("account balances not conserved: total=%d", total)
		}
	})
}

package ledger

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"payment-sim/internal/httpx"
	"payment-sim/internal/ids"
)

type Account struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Currency  string    `json:"currency"`
	Balance   int64     `json:"balance"`
	CreatedAt time.Time `json:"created_at"`
}

type Entry struct {
	AccountID string `json:"account_id"`
	Amount    int64  `json:"amount"`
}

type Posting struct {
	ID          string    `json:"id"`
	Currency    string    `json:"currency"`
	Description string    `json:"description"`
	Entries     []Entry   `json:"entries"`
	CreatedAt   time.Time `json:"created_at"`
}

type CreateAccount struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	Currency string `json:"currency"`
}

type CreatePosting struct {
	Currency    string  `json:"currency"`
	Description string  `json:"description"`
	Entries     []Entry `json:"entries"`
}

type idempotencyRecord struct {
	Hash      [32]byte
	Operation string
	Account   Account
	Posting   Posting
}

type Store struct {
	mu       sync.Mutex
	accounts map[string]Account
	postings []Posting
	idem     map[string]idempotencyRecord
}

func NewStore() *Store {
	return &Store{accounts: make(map[string]Account), idem: make(map[string]idempotencyRecord)}
}

func currency(value string) (string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) != 3 {
		return "", httpx.Err(http.StatusBadRequest, "invalid_currency", "currency must be a three-letter code")
	}
	for _, r := range value {
		if r < 'A' || r > 'Z' {
			return "", httpx.Err(http.StatusBadRequest, "invalid_currency", "currency must be a three-letter code")
		}
	}
	return value, nil
}

func fingerprint(value any) [32]byte {
	encoded, _ := json.Marshal(value)
	return sha256.Sum256(encoded)
}

func (s *Store) CreateAccount(key string, request CreateAccount) (Account, error) {
	if err := validateIdempotencyKey(key); err != nil {
		return Account{}, err
	}
	request.Name = strings.TrimSpace(request.Name)
	request.ID = strings.TrimSpace(request.ID)
	cur, err := currency(request.Currency)
	if err != nil {
		return Account{}, err
	}
	request.Currency = cur
	if request.Name == "" {
		return Account{}, httpx.Err(http.StatusBadRequest, "invalid_name", "name is required")
	}
	if len(request.Name) > 200 {
		return Account{}, httpx.Err(http.StatusBadRequest, "invalid_name", "name must be at most 200 bytes")
	}
	if len(request.ID) > 200 {
		return Account{}, httpx.Err(http.StatusBadRequest, "invalid_account_id", "account id must be at most 200 bytes")
	}
	requestHash := fingerprint(struct {
		Operation string
		Request   CreateAccount
	}{"account", request})

	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.idem[key]; ok {
		if previous.Hash != requestHash || previous.Operation != "account" {
			return Account{}, idempotencyConflict()
		}
		return previous.Account, nil
	}
	id := request.ID
	if id == "" {
		id = ids.New("acct")
	}
	if _, ok := s.accounts[id]; ok {
		return Account{}, httpx.Err(http.StatusConflict, "account_exists", "account already exists")
	}
	account := Account{ID: id, Name: request.Name, Currency: cur, CreatedAt: time.Now().UTC()}
	s.accounts[id] = account
	s.idem[key] = idempotencyRecord{Hash: requestHash, Operation: "account", Account: account}
	return account, nil
}

func (s *Store) CreatePosting(key string, request CreatePosting) (Posting, error) {
	if err := validateIdempotencyKey(key); err != nil {
		return Posting{}, err
	}
	cur, err := currency(request.Currency)
	if err != nil {
		return Posting{}, err
	}
	request.Currency = cur
	request.Description = strings.TrimSpace(request.Description)
	request.Entries = append([]Entry(nil), request.Entries...)
	for i := range request.Entries {
		request.Entries[i].AccountID = strings.TrimSpace(request.Entries[i].AccountID)
	}
	if len(request.Description) > 500 {
		return Posting{}, httpx.Err(http.StatusBadRequest, "invalid_description", "description must be at most 500 bytes")
	}
	if len(request.Entries) < 2 {
		return Posting{}, httpx.Err(http.StatusBadRequest, "unbalanced_posting", "a posting requires at least two entries")
	}
	var sum big.Int
	for _, entry := range request.Entries {
		if entry.AccountID == "" || entry.Amount == 0 {
			return Posting{}, httpx.Err(http.StatusBadRequest, "invalid_entry", "entry account_id and non-zero amount are required")
		}
		if len(entry.AccountID) > 200 {
			return Posting{}, httpx.Err(http.StatusBadRequest, "invalid_entry", "entry account_id must be at most 200 bytes")
		}
		sum.Add(&sum, big.NewInt(entry.Amount))
	}
	if sum.Sign() != 0 {
		return Posting{}, httpx.Err(http.StatusBadRequest, "unbalanced_posting", "entry amounts must sum to zero")
	}
	requestHash := fingerprint(struct {
		Operation string
		Request   CreatePosting
	}{"posting", request})

	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.idem[key]; ok {
		if previous.Hash != requestHash || previous.Operation != "posting" {
			return Posting{}, idempotencyConflict()
		}
		return clonePosting(previous.Posting), nil
	}

	exactDeltas := make(map[string]*big.Int)
	for _, entry := range request.Entries {
		account, ok := s.accounts[entry.AccountID]
		if !ok {
			return Posting{}, httpx.Err(http.StatusBadRequest, "account_not_found", fmt.Sprintf("account %s not found", entry.AccountID))
		}
		if account.Currency != cur {
			return Posting{}, httpx.Err(http.StatusBadRequest, "currency_mismatch", "entry account currency does not match posting")
		}
		if exactDeltas[entry.AccountID] == nil {
			exactDeltas[entry.AccountID] = new(big.Int)
		}
		exactDeltas[entry.AccountID].Add(exactDeltas[entry.AccountID], big.NewInt(entry.Amount))
	}
	deltas := make(map[string]int64, len(exactDeltas))
	for accountID, exact := range exactDeltas {
		if !exact.IsInt64() {
			return Posting{}, httpx.Err(http.StatusBadRequest, "amount_overflow", "account delta overflows int64")
		}
		deltas[accountID] = exact.Int64()
	}
	for accountID, delta := range deltas {
		if _, ok := checkedAdd(s.accounts[accountID].Balance, delta); !ok {
			return Posting{}, httpx.Err(http.StatusBadRequest, "balance_overflow", "posting would overflow an account balance")
		}
	}

	posting := Posting{ID: ids.New("post"), Currency: cur, Description: request.Description, Entries: append([]Entry(nil), request.Entries...), CreatedAt: time.Now().UTC()}
	for accountID, delta := range deltas {
		account := s.accounts[accountID]
		account.Balance += delta
		s.accounts[accountID] = account
	}
	s.postings = append(s.postings, posting)
	s.idem[key] = idempotencyRecord{Hash: requestHash, Operation: "posting", Posting: clonePosting(posting)}
	return clonePosting(posting), nil
}

func checkedAdd(left, right int64) (int64, bool) {
	if right > 0 && left > math.MaxInt64-right {
		return 0, false
	}
	if right < 0 && left < math.MinInt64-right {
		return 0, false
	}
	return left + right, true
}

func clonePosting(posting Posting) Posting {
	posting.Entries = append([]Entry(nil), posting.Entries...)
	return posting
}

func validateIdempotencyKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return httpx.Err(http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
	}
	if len(key) > 200 {
		return httpx.Err(http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be at most 200 bytes")
	}
	return nil
}

func idempotencyConflict() error {
	return httpx.Err(http.StatusConflict, "idempotency_conflict", "key was already used with a different request")
}

func (s *Store) Accounts() []Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	accounts := make([]Account, 0, len(s.accounts))
	for _, account := range s.accounts {
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
	return accounts
}

func (s *Store) Account(id string) (Account, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.accounts[id]
	return account, ok
}

func (s *Store) Postings() []Posting {
	s.mu.Lock()
	defer s.mu.Unlock()
	postings := make([]Posting, len(s.postings))
	for i, posting := range s.postings {
		postings[i] = clonePosting(posting)
	}
	return postings
}

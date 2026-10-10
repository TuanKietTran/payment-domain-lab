package payment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"payment-sim/internal/httpx"
	"payment-sim/internal/ids"
)

type Status string

const (
	StatusCreated           Status = "created"
	StatusAuthorized        Status = "authorized"
	StatusCaptured          Status = "captured"
	StatusPartiallyRefunded Status = "partially_refunded"
	StatusRefunded          Status = "refunded"
	StatusCanceled          Status = "canceled"
)

type Payment struct {
	ID             string    `json:"id"`
	Amount         int64     `json:"amount"`
	Currency       string    `json:"currency"`
	Description    string    `json:"description"`
	Status         Status    `json:"status"`
	RefundedAmount int64     `json:"refunded_amount"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type CreatePayment struct {
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
	Description string `json:"description"`
}

type Action struct {
	Amount int64 `json:"amount,omitempty"`
}

type LedgerEntry struct {
	AccountID string `json:"account_id"`
	Amount    int64  `json:"amount"`
}

type LedgerPosting struct {
	Currency    string        `json:"currency"`
	Description string        `json:"description"`
	Entries     []LedgerEntry `json:"entries"`
}

type Ledger interface {
	Post(context.Context, string, LedgerPosting) error
}

type idempotencyRecord struct {
	Hash    [32]byte
	Payment Payment
}

type Store struct {
	mu       sync.Mutex
	ledger   Ledger
	payments map[string]Payment
	idem     map[string]idempotencyRecord
}

func NewStore(client Ledger) *Store {
	return &Store{ledger: client, payments: make(map[string]Payment), idem: make(map[string]idempotencyRecord)}
}

func normalizeCurrency(value string) (string, error) {
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

func hash(value any) [32]byte {
	encoded, _ := json.Marshal(value)
	return sha256.Sum256(encoded)
}

func (s *Store) Create(key string, request CreatePayment) (Payment, error) {
	if err := validateIdempotencyKey(key); err != nil {
		return Payment{}, err
	}
	currency, err := normalizeCurrency(request.Currency)
	if err != nil {
		return Payment{}, err
	}
	request.Currency = currency
	request.Description = strings.TrimSpace(request.Description)
	if len(request.Description) > 200 {
		return Payment{}, httpx.Err(http.StatusBadRequest, "invalid_description", "description must be at most 200 bytes")
	}
	if request.Amount <= 0 {
		return Payment{}, httpx.Err(http.StatusBadRequest, "invalid_amount", "amount must be a positive integer in minor units")
	}

	fingerprint := hash(struct {
		Operation string
		Request   CreatePayment
	}{"create", request})
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.idem[key]; ok {
		if previous.Hash != fingerprint {
			return Payment{}, idempotencyConflict()
		}
		return previous.Payment, nil
	}
	now := time.Now().UTC()
	payment := Payment{ID: ids.New("pay"), Amount: request.Amount, Currency: currency, Description: request.Description, Status: StatusCreated, CreatedAt: now, UpdatedAt: now}
	s.payments[payment.ID] = payment
	s.idem[key] = idempotencyRecord{Hash: fingerprint, Payment: payment}
	return payment, nil
}

func (s *Store) Act(ctx context.Context, id, operation, key string, request Action) (Payment, error) {
	if err := validateIdempotencyKey(key); err != nil {
		return Payment{}, err
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	fingerprint := hash(struct {
		Operation string
		PaymentID string
		Request   Action
	}{operation, id, request})

	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.idem[key]; ok {
		if previous.Hash != fingerprint {
			return Payment{}, idempotencyConflict()
		}
		return previous.Payment, nil
	}
	current, ok := s.payments[id]
	if !ok {
		return Payment{}, httpx.Err(http.StatusNotFound, "payment_not_found", "payment not found")
	}

	updated := current
	var posting *LedgerPosting
	switch operation {
	case "authorize":
		if request.Amount != 0 {
			return Payment{}, httpx.Err(http.StatusBadRequest, "invalid_amount", "authorize does not accept an amount")
		}
		if current.Status != StatusCreated {
			return Payment{}, invalidTransition(current.Status, operation)
		}
		updated.Status = StatusAuthorized
		posting = transfer(current, "authorize payment", customerAccount(current.Currency), pendingAccount(current.Currency), current.Amount)
	case "capture":
		if request.Amount != 0 {
			return Payment{}, httpx.Err(http.StatusBadRequest, "invalid_amount", "capture does not accept an amount")
		}
		if current.Status != StatusAuthorized {
			return Payment{}, invalidTransition(current.Status, operation)
		}
		updated.Status = StatusCaptured
		posting = transfer(current, "capture payment", pendingAccount(current.Currency), settledAccount(current.Currency), current.Amount)
	case "cancel":
		if request.Amount != 0 {
			return Payment{}, httpx.Err(http.StatusBadRequest, "invalid_amount", "cancel does not accept an amount")
		}
		switch current.Status {
		case StatusCreated:
			updated.Status = StatusCanceled
		case StatusAuthorized:
			updated.Status = StatusCanceled
			posting = transfer(current, "cancel authorization", pendingAccount(current.Currency), customerAccount(current.Currency), current.Amount)
		default:
			return Payment{}, invalidTransition(current.Status, operation)
		}
	case "refund":
		if current.Status != StatusCaptured && current.Status != StatusPartiallyRefunded {
			return Payment{}, invalidTransition(current.Status, operation)
		}
		if request.Amount <= 0 {
			return Payment{}, httpx.Err(http.StatusBadRequest, "invalid_amount", "refund amount must be positive")
		}
		if request.Amount > current.Amount-current.RefundedAmount {
			return Payment{}, httpx.Err(http.StatusBadRequest, "refund_exceeds_remaining", "refund amount exceeds the unrefunded amount")
		}
		if current.RefundedAmount > math.MaxInt64-request.Amount {
			return Payment{}, httpx.Err(http.StatusBadRequest, "amount_overflow", "refunded amount overflows int64")
		}
		updated.RefundedAmount += request.Amount
		if updated.RefundedAmount == updated.Amount {
			updated.Status = StatusRefunded
		} else {
			updated.Status = StatusPartiallyRefunded
		}
		posting = transfer(current, "refund payment", settledAccount(current.Currency), customerAccount(current.Currency), request.Amount)
	default:
		return Payment{}, httpx.Err(http.StatusNotFound, "action_not_found", "payment action not found")
	}

	if posting != nil {
		if s.ledger == nil {
			return Payment{}, httpx.Err(http.StatusServiceUnavailable, "ledger_unavailable", "ledger service is unavailable")
		}
		if err := s.ledger.Post(ctx, "payment:"+key, *posting); err != nil {
			return Payment{}, err
		}
	}
	updated.UpdatedAt = time.Now().UTC()
	s.payments[id] = updated
	s.idem[key] = idempotencyRecord{Hash: fingerprint, Payment: updated}
	return updated, nil
}

func transfer(payment Payment, description, from, to string, amount int64) *LedgerPosting {
	return &LedgerPosting{
		Currency:    payment.Currency,
		Description: description + " " + payment.ID,
		Entries: []LedgerEntry{
			{AccountID: from, Amount: -amount},
			{AccountID: to, Amount: amount},
		},
	}
}

func customerAccount(currency string) string { return "customer_payable_" + currency }
func pendingAccount(currency string) string  { return "merchant_pending_" + currency }
func settledAccount(currency string) string  { return "merchant_settled_" + currency }

func (s *Store) Get(id string) (Payment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	payment, ok := s.payments[id]
	return payment, ok
}

func (s *Store) List() []Payment {
	s.mu.Lock()
	defer s.mu.Unlock()
	payments := make([]Payment, 0, len(s.payments))
	for _, payment := range s.payments {
		payments = append(payments, payment)
	}
	sort.Slice(payments, func(i, j int) bool {
		if payments[i].CreatedAt.Equal(payments[j].CreatedAt) {
			return payments[i].ID < payments[j].ID
		}
		return payments[i].CreatedAt.After(payments[j].CreatedAt)
	})
	return payments
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

func invalidTransition(status Status, operation string) error {
	return httpx.Err(http.StatusConflict, "invalid_transition", "cannot "+operation+" a payment in "+string(status)+" state")
}

package ledger

import (
	"log/slog"
	"net/http"

	"payment-sim/internal/httpx"
)

func Handler(store *Store, logger *slog.Logger) http.Handler {
	return NewHandler(store, logger, "")
}

// NewHandler serves the ledger API. A non-empty token requires bearer auth on
// every route except /healthz.
func NewHandler(store *Store, logger *slog.Logger, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !httpx.Method(w, r, http.MethodGet) {
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "ledger-service"})
	})
	mux.HandleFunc("/v1/accounts", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			limit, cursor, err := httpx.PageParams(r)
			if err != nil {
				httpx.WriteError(w, err)
				return
			}
			page, next, err := httpx.Paginate(store.Accounts(), limit, cursor, func(a Account) string { return a.ID })
			if err != nil {
				httpx.WriteError(w, err)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, httpx.PageBody("accounts", page, next))
		case http.MethodPost:
			var request CreateAccount
			if err := httpx.Decode(w, r, &request); err != nil {
				httpx.WriteError(w, err)
				return
			}
			account, err := store.CreateAccount(r.Header.Get("Idempotency-Key"), request)
			if err != nil {
				httpx.WriteError(w, err)
				return
			}
			httpx.WriteJSON(w, http.StatusCreated, account)
		default:
			httpx.Method(w, r, http.MethodGet, http.MethodPost)
		}
	})
	mux.HandleFunc("/v1/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !httpx.Method(w, r, http.MethodGet) {
			return
		}
		account, ok := store.Account(r.PathValue("id"))
		if !ok {
			httpx.WriteError(w, httpx.Err(http.StatusNotFound, "account_not_found", "account not found"))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, account)
	})
	mux.HandleFunc("/v1/postings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			limit, cursor, err := httpx.PageParams(r)
			if err != nil {
				httpx.WriteError(w, err)
				return
			}
			page, next, err := httpx.Paginate(store.Postings(), limit, cursor, func(p Posting) string { return p.ID })
			if err != nil {
				httpx.WriteError(w, err)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, httpx.PageBody("postings", page, next))
		case http.MethodPost:
			var request CreatePosting
			if err := httpx.Decode(w, r, &request); err != nil {
				httpx.WriteError(w, err)
				return
			}
			posting, err := store.CreatePosting(r.Header.Get("Idempotency-Key"), request)
			if err != nil {
				httpx.WriteError(w, err)
				return
			}
			httpx.WriteJSON(w, http.StatusCreated, posting)
		default:
			httpx.Method(w, r, http.MethodGet, http.MethodPost)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, httpx.Err(http.StatusNotFound, "not_found", "resource not found"))
	})
	return httpx.Middleware(logger, httpx.BearerAuth(token, mux, "/healthz"))
}

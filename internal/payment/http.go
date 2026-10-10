package payment

import (
	"embed"
	"log/slog"
	"net/http"
	"strings"

	"payment-sim/internal/httpx"
)

//go:embed admin.html
var adminFiles embed.FS

func Handler(store *Store, logger *slog.Logger) http.Handler {
	return NewHandler(store, logger, "")
}

// NewHandler serves the payment API and admin. A non-empty token requires
// bearer auth on /v1 routes; the admin page and /healthz stay open so the page
// can load and prompt for the token.
func NewHandler(store *Store, logger *slog.Logger, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		if !httpx.Method(w, r, http.MethodGet) {
			return
		}
		page, err := adminFiles.ReadFile("admin.html")
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(page)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !httpx.Method(w, r, http.MethodGet) {
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "payment-service"})
	})
	mux.HandleFunc("/v1/payments", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			limit, cursor, err := httpx.PageParams(r)
			if err != nil {
				httpx.WriteError(w, err)
				return
			}
			page, next, err := httpx.Paginate(store.List(), limit, cursor, func(p Payment) string { return p.ID })
			if err != nil {
				httpx.WriteError(w, err)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, httpx.PageBody("payments", page, next))
		case http.MethodPost:
			var request CreatePayment
			if err := httpx.Decode(w, r, &request); err != nil {
				httpx.WriteError(w, err)
				return
			}
			result, err := store.Create(r.Header.Get("Idempotency-Key"), request)
			if err != nil {
				httpx.WriteError(w, err)
				return
			}
			httpx.WriteJSON(w, http.StatusCreated, result)
		default:
			w.Header().Set("Allow", "GET, POST")
			httpx.WriteError(w, httpx.Err(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed"))
		}
	})
	mux.HandleFunc("/v1/payments/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !httpx.Method(w, r, http.MethodGet) {
			return
		}
		payment, ok := store.Get(r.PathValue("id"))
		if !ok {
			httpx.WriteError(w, httpx.Err(http.StatusNotFound, "payment_not_found", "payment not found"))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, payment)
	})
	mux.HandleFunc("/v1/payments/{id}/actions/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !httpx.Method(w, r, http.MethodPost) {
			return
		}
		var request Action
		if err := httpx.Decode(w, r, &request); err != nil {
			httpx.WriteError(w, err)
			return
		}
		result, err := store.Act(r.Context(), r.PathValue("id"), r.PathValue("action"), r.Header.Get("Idempotency-Key"), request)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/healthz" {
			httpx.WriteError(w, httpx.Err(http.StatusNotFound, "not_found", "resource not found"))
			return
		}
		httpx.WriteError(w, httpx.Err(http.StatusNotFound, "not_found", "page not found"))
	})
	return httpx.Middleware(logger, httpx.BearerAuth(token, mux, "/healthz", "/"))
}

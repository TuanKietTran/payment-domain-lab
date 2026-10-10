package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const maxRequestBody = 1 << 20

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

func (e *APIError) Error() string { return e.Message }

func Err(status int, code, message string) *APIError {
	return &APIError{Code: code, Message: message, Status: status}
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func WriteError(w http.ResponseWriter, err error) {
	var api *APIError
	if !errors.As(err, &api) {
		api = Err(http.StatusInternalServerError, "internal_error", "internal server error")
	}
	WriteJSON(w, api.Status, map[string]any{"error": api})
}

func Decode(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	dec := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return decodeRequestError(err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err != nil {
			return decodeRequestError(err)
		}
		return Err(http.StatusBadRequest, "invalid_json", "request body must contain one JSON object")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Err(http.StatusBadRequest, "invalid_json", "request body must contain one JSON object")
	}
	if err := rejectDuplicateObjectNames(trimmed); err != nil {
		return err
	}
	object := json.NewDecoder(bytes.NewReader(trimmed))
	object.DisallowUnknownFields()
	if err := object.Decode(dst); err != nil {
		return decodeRequestError(err)
	}
	return nil
}

func rejectDuplicateObjectNames(encoded []byte) error {
	type frame struct {
		object    bool
		expectKey bool
		keys      map[string]struct{}
	}
	var stack []frame
	completeValue := func() {
		if len(stack) == 0 {
			return
		}
		parent := &stack[len(stack)-1]
		if parent.object && !parent.expectKey {
			parent.expectKey = true
		}
	}
	dec := json.NewDecoder(bytes.NewReader(encoded))
	for {
		token, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return decodeRequestError(err)
		}
		switch value := token.(type) {
		case json.Delim:
			switch value {
			case '{':
				stack = append(stack, frame{object: true, expectKey: true, keys: make(map[string]struct{})})
			case '[':
				stack = append(stack, frame{})
			case '}', ']':
				if len(stack) == 0 {
					return Err(http.StatusBadRequest, "invalid_json", "invalid JSON object")
				}
				stack = stack[:len(stack)-1]
				completeValue()
			}
		case string:
			if len(stack) > 0 && stack[len(stack)-1].object && stack[len(stack)-1].expectKey {
				current := &stack[len(stack)-1]
				if _, ok := current.keys[value]; ok {
					return Err(http.StatusBadRequest, "invalid_json", "request body contains duplicate object names")
				}
				current.keys[value] = struct{}{}
				current.expectKey = false
				continue
			}
			completeValue()
		default:
			completeValue()
		}
	}
}

func decodeRequestError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return Err(http.StatusRequestEntityTooLarge, "request_too_large", fmt.Sprintf("request body must not exceed %d bytes", maxRequestBody))
	}
	return Err(http.StatusBadRequest, "invalid_json", fmt.Sprintf("invalid request body: %v", err))
}

type bufferedResponse struct {
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: make(http.Header), status: http.StatusOK}
}

func (w *bufferedResponse) Header() http.Header { return w.header }

func (w *bufferedResponse) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
}

func (w *bufferedResponse) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(body)
}

func (w *bufferedResponse) flush(target http.ResponseWriter) {
	for key, values := range w.header {
		target.Header()[key] = append([]string(nil), values...)
	}
	target.WriteHeader(w.status)
	_, _ = target.Write(w.body.Bytes())
}

func Middleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := requestIDFrom(r.Header.Get(RequestIDHeader))
		r = r.WithContext(withRequestID(r.Context(), id))
		buffer := newBufferedResponse()
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("panic", "request_id", id, "error", recovered)
				buffer = newBufferedResponse()
				WriteError(buffer, errors.New("panic"))
			}
			// Set middleware-owned headers on the buffered response after the
			// handler finishes so a handler cannot replace them.
			buffer.Header().Set("X-Content-Type-Options", "nosniff")
			buffer.Header().Set(RequestIDHeader, id)
			buffer.flush(w)
			logger.Info("request", "request_id", id, "method", r.Method, "path", r.URL.Path, "status", buffer.status, "bytes", buffer.body.Len(), "remote_addr", r.RemoteAddr, "duration_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(buffer, r)
	})
}

func Method(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	for _, method := range allowed {
		if r.Method == method {
			return true
		}
	}
	w.Header().Set("Allow", joinMethods(allowed))
	WriteError(w, Err(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed"))
	return false
}

func joinMethods(methods []string) string {
	var result string
	for i, method := range methods {
		if i > 0 {
			result += ", "
		}
		result += method
	}
	return result
}

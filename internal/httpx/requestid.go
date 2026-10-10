package httpx

import (
	"context"

	"payment-sim/internal/ids"
)

const RequestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// RequestID returns the request ID stored by Middleware, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// requestIDFrom accepts a caller-supplied ID only when short and made of safe
// characters; otherwise it generates one.
func requestIDFrom(supplied string) string {
	if supplied == "" || len(supplied) > 64 {
		return ids.New("req")
	}
	for _, r := range supplied {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
		if !ok {
			return ids.New("req")
		}
	}
	return supplied
}

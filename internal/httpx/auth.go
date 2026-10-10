package httpx

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// BearerAuth requires "Authorization: Bearer <token>" on every request except
// exact paths listed in exempt. An empty token disables auth.
func BearerAuth(token string, next http.Handler, exempt ...string) http.Handler {
	if token == "" {
		return next
	}
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, path := range exempt {
			if r.URL.Path == path {
				next.ServeHTTP(w, r)
				return
			}
		}
		value, valid := bearerCredential(r.Header.Values("Authorization"))
		got := sha256.Sum256([]byte(value))
		// Hashing first keeps the secret comparison constant-time regardless of
		// credential length. Compute it even when the syntax is invalid.
		matched := subtle.ConstantTimeCompare(got[:], want[:])
		if !valid || matched != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="payment-sim"`)
			WriteError(w, Err(http.StatusUnauthorized, "unauthorized", "a valid bearer token is required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerCredential accepts the RFC auth-scheme form with one or more SP
// characters followed by one token68 credential. It deliberately rejects
// leading/trailing whitespace, tabs, trailing content, and multiple header
// field values so ambiguous credentials cannot be interpreted differently by
// intermediaries.
func bearerCredential(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	header := values[0]
	separator := strings.IndexByte(header, ' ')
	if separator <= 0 || !strings.EqualFold(header[:separator], "Bearer") {
		return "", false
	}
	credential := header[separator:]
	for len(credential) > 0 && credential[0] == ' ' {
		credential = credential[1:]
	}
	if credential == "" {
		return "", false
	}

	padding := false
	for _, char := range []byte(credential) {
		if char == '=' {
			padding = true
			continue
		}
		if padding || !isToken68Character(char) {
			return credential, false
		}
	}
	return credential, true
}

func isToken68Character(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
		char >= '0' && char <= '9' || strings.ContainsRune("-._~+/", rune(char))
}

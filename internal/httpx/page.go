package httpx

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
)

const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
	maxCursorLength  = 400
)

// PageParams parses the bounded limit and opaque cursor query parameters.
func PageParams(r *http.Request) (int, string, error) {
	query := r.URL.Query()
	limit := DefaultPageLimit
	if value := query.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > MaxPageLimit {
			return 0, "", Err(http.StatusBadRequest, "invalid_limit", fmt.Sprintf("limit must be an integer between 1 and %d", MaxPageLimit))
		}
		limit = parsed
	}
	cursor := query.Get("cursor")
	if len(cursor) > maxCursorLength {
		return 0, "", InvalidCursor()
	}
	return limit, cursor, nil
}

func EncodeCursor(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

// DecodeCursor returns the empty string for an empty cursor.
func DecodeCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(decoded) == 0 {
		return "", InvalidCursor()
	}
	return string(decoded), nil
}

func InvalidCursor() error {
	return Err(http.StatusBadRequest, "invalid_cursor", "cursor is invalid")
}

// PageBody builds a list response with an optional next_cursor.
func PageBody(key string, items any, next string) map[string]any {
	body := map[string]any{key: items}
	if next != "" {
		body["next_cursor"] = next
	}
	return body
}

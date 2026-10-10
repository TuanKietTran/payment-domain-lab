package httpx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBearerAuth(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := BearerAuth("secret", ok, "/healthz")
	cases := []struct {
		name    string
		path    string
		headers []string
		want    int
	}{
		{name: "exempt", path: "/healthz", want: http.StatusNoContent},
		{name: "missing", path: "/v1/x", want: http.StatusUnauthorized},
		{name: "wrong", path: "/v1/x", headers: []string{"Bearer nope"}, want: http.StatusUnauthorized},
		{name: "wrong scheme", path: "/v1/x", headers: []string{"Basic secret"}, want: http.StatusUnauthorized},
		{name: "prefix", path: "/v1/x", headers: []string{"Bearer secre"}, want: http.StatusUnauthorized},
		{name: "leading whitespace", path: "/v1/x", headers: []string{" Bearer secret"}, want: http.StatusUnauthorized},
		{name: "tab separator", path: "/v1/x", headers: []string{"Bearer\tsecret"}, want: http.StatusUnauthorized},
		{name: "trailing whitespace", path: "/v1/x", headers: []string{"Bearer secret "}, want: http.StatusUnauthorized},
		{name: "trailing content", path: "/v1/x", headers: []string{"Bearer secret extra"}, want: http.StatusUnauthorized},
		{name: "comma joined", path: "/v1/x", headers: []string{"Bearer secret, Bearer secret"}, want: http.StatusUnauthorized},
		{name: "duplicate fields", path: "/v1/x", headers: []string{"Bearer secret", "Bearer secret"}, want: http.StatusUnauthorized},
		{name: "valid", path: "/v1/x", headers: []string{"Bearer secret"}, want: http.StatusNoContent},
		{name: "scheme case", path: "/v1/x", headers: []string{"bearer secret"}, want: http.StatusNoContent},
		{name: "multiple RFC spaces", path: "/v1/x", headers: []string{"Bearer   secret"}, want: http.StatusNoContent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, c.path, nil)
			for _, value := range c.headers {
				request.Header.Add("Authorization", value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != c.want {
				t.Errorf("status=%d want %d", response.Code, c.want)
			}
			if c.want == http.StatusUnauthorized && response.Header().Get("WWW-Authenticate") == "" {
				t.Error("missing WWW-Authenticate")
			}
		})
	}
}

func TestBearerAuthDisabledWhenTokenEmpty(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	response := httptest.NewRecorder()
	BearerAuth("", ok).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/x", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestPageParams(t *testing.T) {
	limit, cursor, err := PageParams(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil || limit != DefaultPageLimit || cursor != "" {
		t.Fatalf("defaults: %d %q %v", limit, cursor, err)
	}
	for _, query := range []string{"limit=0", "limit=-1", "limit=abc", "limit=201", "cursor=" + strings.Repeat("a", 401)} {
		if _, _, err := PageParams(httptest.NewRequest(http.MethodGet, "/?"+query, nil)); err == nil {
			t.Errorf("%s: expected error", query)
		}
	}
	if limit, _, err := PageParams(httptest.NewRequest(http.MethodGet, "/?limit=200", nil)); err != nil || limit != 200 {
		t.Fatalf("max limit: %d %v", limit, err)
	}
}

func TestPaginate(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}
	id := func(s string) string { return s }
	var got []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		page, next, err := Paginate(items, 2, cursor, id)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page...)
		if next == "" {
			break
		}
		cursor = next
	}
	if strings.Join(got, "") != "abcde" {
		t.Fatalf("got %v", got)
	}
	if page, next, _ := Paginate(items, 5, "", id); len(page) != 5 || next != "" {
		t.Fatalf("exact fit: %v %q", page, next)
	}
	if page, next, err := Paginate([]string(nil), 5, "", id); err != nil || page == nil || len(page) != 0 || next != "" {
		t.Fatalf("empty: %v %q %v", page, next, err)
	}
	for _, bad := range []string{"!!!", EncodeCursor("zzz")} {
		if _, _, err := Paginate(items, 2, bad, id); err == nil {
			t.Errorf("cursor %q: expected error", bad)
		}
	}
}

func TestPaginateRandomizedTraversalAndTampering(t *testing.T) {
	seed := int64(20241012)
	rng := rand.New(rand.NewSource(seed))
	for scenario := 0; scenario < 200; scenario++ {
		length := rng.Intn(100)
		limit := rng.Intn(MaxPageLimit) + 1
		items := make([]string, length)
		for i := range items {
			items[i] = fmt.Sprintf("id-%03d", i)
		}
		var got []string
		seenCursors := map[string]struct{}{}
		cursor := ""
		for pages := 0; pages <= length+1; pages++ {
			page, next, err := Paginate(items, limit, cursor, func(s string) string { return s })
			if err != nil {
				t.Fatalf("seed=%d scenario=%d cursor=%q: %v", seed, scenario, cursor, err)
			}
			got = append(got, page...)
			if next == "" {
				break
			}
			if _, duplicate := seenCursors[next]; duplicate {
				t.Fatalf("seed=%d scenario=%d repeated cursor %q", seed, scenario, next)
			}
			seenCursors[next] = struct{}{}
			cursor = next
		}
		if strings.Join(got, ",") != strings.Join(items, ",") {
			t.Fatalf("seed=%d scenario=%d limit=%d got=%v want=%v", seed, scenario, limit, got, items)
		}
		if length > 0 {
			valid := EncodeCursor(items[rng.Intn(length)])
			tampered := valid[:len(valid)-1] + "_"
			if tampered != valid {
				_, _, _ = Paginate(items, limit, valid, func(s string) string { return s })
				if _, _, err := Paginate(items, limit, tampered, func(s string) string { return s }); err == nil {
					t.Fatalf("seed=%d scenario=%d tampered cursor %q accepted", seed, scenario, tampered)
				}
			}
		}
	}
}

func TestRequestIDHeaderAndLogFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	var seen string
	handler := Middleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestID(r.Context())
		w.WriteHeader(http.StatusAccepted)
	}))

	request := httptest.NewRequest(http.MethodGet, "/thing", nil)
	request.Header.Set(RequestIDHeader, "client-id.1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get(RequestIDHeader) != "client-id.1" || seen != "client-id.1" {
		t.Fatalf("header=%q ctx=%q", response.Header().Get(RequestIDHeader), seen)
	}
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatalf("log not JSON: %v: %q", err, buf.String())
	}
	if entry["request_id"] != "client-id.1" || entry["method"] != "GET" || entry["path"] != "/thing" || entry["status"] != float64(202) {
		t.Fatalf("log fields: %v", entry)
	}
	for _, field := range []string{"duration_ms", "bytes", "remote_addr"} {
		if _, ok := entry[field]; !ok {
			t.Errorf("log missing %s", field)
		}
	}
}

func TestRequestIDRejectsUnsafeSuppliedValues(t *testing.T) {
	handler := Middleware(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, supplied := range []string{"bad id", "x\ty", strings.Repeat("a", 65), "é"} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set(RequestIDHeader, supplied)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		got := response.Header().Get(RequestIDHeader)
		if got == supplied || !strings.HasPrefix(got, "req_") {
			t.Errorf("supplied %q -> %q", supplied, got)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.HasPrefix(response.Header().Get(RequestIDHeader), "req_") {
		t.Fatalf("generated id missing")
	}
}

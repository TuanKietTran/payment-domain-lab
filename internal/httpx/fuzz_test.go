package httpx

import (
	"bytes"
	"net/http/httptest"
	"testing"
)

func FuzzDecode(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{}`),
		[]byte(`{"value":"hello"}`),
		[]byte(`{"value":"` + string(make([]byte, 512)) + `"}`),
		[]byte(`{"value":"first","value":"second"}`),
		[]byte(`{} {}`),
		[]byte(`null`),
		[]byte(`[]`),
		[]byte(`"string"`),
		[]byte(`1`),
		[]byte(`true`),
		[]byte(`{"nested":{"id":1,"id":2}}`),
		[]byte(``),
		[]byte(`   {   }   `),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var dst struct {
			Value string `json:"value"`
		}
		r := httptest.NewRequest("POST", "/", bytes.NewReader(data))
		w := httptest.NewRecorder()
		err := Decode(w, r, &dst)
		if err != nil {
			var api *APIError
			if !isAPIError(err, &api) {
				t.Fatalf("non-APIError returned: %T %v", err, err)
			}
			if api.Status < 400 || api.Status > 599 {
				t.Fatalf("APIError has out-of-range status %d", api.Status)
			}
			if api.Code == "" {
				t.Fatalf("APIError has empty code")
			}
		}
	})
}

func isAPIError(err error, out **APIError) bool {
	if err == nil {
		return false
	}
	if api, ok := err.(*APIError); ok {
		*out = api
		return true
	}
	return false
}

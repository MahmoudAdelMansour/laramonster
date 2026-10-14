package ipc

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	cases := []struct {
		name, token, sent string
		want              int
	}{
		{"unix: no token, open", "", "", http.StatusNoContent},
		{"windows: right token", "s3cret", "s3cret", http.StatusNoContent},
		{"windows: no token", "s3cret", "", http.StatusForbidden},
		{"windows: wrong token", "s3cret", "guess", http.StatusForbidden},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/status", nil)
		if c.sent != "" {
			r.Header.Set(Header, c.sent)
		}
		w := httptest.NewRecorder()
		Guard(c.token, ok).ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, w.Code, c.want)
		}
	}
}

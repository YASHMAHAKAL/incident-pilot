package investigation

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPAuthentication(t *testing.T) {
	t.Parallel()
	server, err := NewServer(Backend{}, strings.Repeat("x", 24), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		auth string
		code int
	}{{"", 401}, {"Bearer bad", 401}, {"Bearer " + strings.Repeat("x", 24), 415}} {
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
		if tc.auth != "" {
			r.Header.Set("Authorization", tc.auth)
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Errorf("auth %q: got %d want %d", tc.auth, w.Code, tc.code)
		}
	}
}

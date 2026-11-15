package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRedirectToHTTPSKeepsThePublicHost covers the plain-HTTP listener: the
// request host is preserved, the port is replaced with the one the TLS listener
// uses, and 443 stays implicit.
func TestRedirectToHTTPSKeepsThePublicHost(t *testing.T) {
	cases := []struct {
		name      string
		httpsPort string
		host      string
		want      string
	}{
		{"default port", "443", "together.example.com", "https://together.example.com/api/health?x=1"},
		{"default port given", "443", "together.example.com:80", "https://together.example.com/api/health?x=1"},
		{"custom port", "8443", "together.example.com:8080", "https://together.example.com:8443/api/health?x=1"},
		{"custom port without host port", "8443", "together.example.com", "https://together.example.com:8443/api/health?x=1"},
		{"ipv6 default port", "443", "[::1]:80", "https://[::1]/api/health?x=1"},
		{"ipv6 custom port", "8443", "[::1]:8080", "https://[::1]:8443/api/health?x=1"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/health?x=1", nil)
			request.Host = test.host
			recorder := httptest.NewRecorder()
			redirectHandler(test.httpsPort)(recorder, request)
			if recorder.Code != http.StatusPermanentRedirect {
				t.Fatalf("status: %d", recorder.Code)
			}
			if got := recorder.Header().Get("Location"); got != test.want {
				t.Fatalf("location: %q, want %q", got, test.want)
			}
		})
	}
}

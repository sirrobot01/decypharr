package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsAPIRequest(t *testing.T) {
	tests := []struct {
		name    string
		urlBase string
		path    string
		want    bool
	}{
		{name: "API", urlBase: "/", path: "/api/repair/run", want: true},
		{name: "webhook", urlBase: "/", path: "/webhooks/tautulli", want: true},
		{name: "web page", urlBase: "/", path: "/login", want: false},
		{name: "API under URL base", urlBase: "/decypharr/", path: "/decypharr/api/repair/run", want: true},
		{name: "webhook under URL base", urlBase: "/decypharr/", path: "/decypharr/webhooks/tautulli", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{urlBase: tt.urlBase}
			r := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if got := s.isAPIRequest(r); got != tt.want {
				t.Fatalf("isAPIRequest() = %t, want %t", got, tt.want)
			}
		})
	}
}

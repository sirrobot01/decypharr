package sabnzbd

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/sirrobot01/decypharr/internal/config"

	"github.com/sirrobot01/decypharr/pkg/arr"
)

type contextKey string

const (
	apiKeyKey   contextKey = "apikey"
	modeKey     contextKey = "mode"
	arrKey      contextKey = "arr"
	categoryKey contextKey = "category"
)

func getMode(ctx context.Context) string {
	if mode, ok := ctx.Value(modeKey).(string); ok {
		return mode
	}
	return ""
}

func (s *SABnzbd) categoryContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		category := r.URL.Query().Get("category")
		if category == "" {
			// Check form data
			_ = r.ParseForm()
			category = r.Form.Get("category")
		}
		if category == "" {
			category = r.FormValue("category")
		}

		ctx := context.WithValue(r.Context(), categoryKey, strings.TrimSpace(category))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func getArrFromContext(ctx context.Context) arr.Arr {
	instance, _ := ctx.Value(arrKey).(arr.Arr)
	return instance
}

func getCategory(ctx context.Context) string {
	if category, ok := ctx.Value(categoryKey).(string); ok {
		return category
	}
	return ""
}

// modeContext extracts the mode parameter from the request
func (s *SABnzbd) modeContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := r.URL.Query().Get("mode")
		if mode == "" {
			// Check form data
			_ = r.ParseForm()
			mode = r.Form.Get("mode")
		}

		// Extract category for Arr integration
		category := r.URL.Query().Get("cat")
		if category == "" {
			category = r.Form.Get("cat")
		}

		downloadUncached := false
		a := arr.Arr{Name: category, DownloadUncached: &downloadUncached, Source: arr.SourceAuto}

		ctx := context.WithValue(r.Context(), modeKey, strings.TrimSpace(mode))
		ctx = context.WithValue(ctx, arrKey, a)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authContext creates a middleware that extracts the Arr host and token from the Authorization header
// and adds it to the request context.
// This is used to identify the Arr instance for the request.
// Only a valid host and token will be added to the context/config. The rest are manual
func (s *SABnzbd) authContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.URL.Query().Get("ma_username")
		token := r.URL.Query().Get("ma_password")
		category := getCategory(r.Context())
		a, err := s.authenticate(r.Context(), category, host, token)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), arrKey, a)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *SABnzbd) authenticate(ctx context.Context, category, username, password string) (arr.Arr, error) {
	cfg := config.Get()
	instance, known := s.manager.Arr().Get(category)
	if !known {
		// Not in the registry yet: inherit download_uncached from a matching
		// config entry so SendToDebrid does not fall back to the provider.
		instance = arr.Arr{Name: category, Host: username, Token: password, Source: arr.SourceAuto}
		for _, configured := range cfg.Arrs {
			if configured.Name == category {
				instance.DownloadUncached = configured.DownloadUncached
				break
			}
		}
	}
	// In token-only mode the arr sends the API token as the password and may
	// leave the username empty.
	if (username == "" || password == "") && cfg.UseAuth && !config.VerifyToken(password) {
		return arr.Arr{}, fmt.Errorf("unauthorized: Host and token are required for authentication(you've enabled authentication)")
	}
	if instance.Source == arr.SourceAuto {
		instance.Host = username
		instance.Token = password
	}

	kind, err := s.manager.Arr().Probe(ctx, instance)
	validated := err == nil
	if validated {
		instance.Type = kind
	}
	if !validated && cfg.UseAuth {
		if !config.VerifyAuth(username, password) && !config.VerifyToken(password) {
			return arr.Arr{}, fmt.Errorf("unauthorized: invalid credentials")
		}
	}
	if username != "" && password != "" {
		s.manager.Arr().AddOrUpdate(instance)
	}
	return instance, nil
}

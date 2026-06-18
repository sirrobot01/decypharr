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

func getArrFromContext(ctx context.Context) *arr.Arr {
	if a, ok := ctx.Value(arrKey).(*arr.Arr); ok {
		return a
	}
	return nil
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

		// Create a default Arr instance for the category
		downloadUncached := false
		a := arr.New(category, "", "", false, &downloadUncached, "", "auto")

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
		a, err := s.authenticate(category, host, token)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), arrKey, a)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *SABnzbd) authenticate(category, username, password string) (*arr.Arr, error) {
	cfg := config.Get()
	a := s.manager.Arr().Get(category)
	if a == nil {
		// Arr is not yet in runtime storage — look for a matching config entry
		// so we inherit its download_uncached setting. If no config match,
		// leave nil so SendToDebrid falls back to the debrid provider's setting.
		var downloadUncached *bool
		for _, cfgArr := range config.Get().Arrs {
			if cfgArr.Name == category {
				downloadUncached = cfgArr.DownloadUncached
				break
			}
		}
		a = arr.New(category, username, password, false, downloadUncached, "", "auto")
	}
	arrValidated := false // This is a flag to indicate if arr validation was successful
	if (username == "" || password == "") && cfg.UseAuth {
		return nil, fmt.Errorf("unauthorized: Host and token are required for authentication(you've enabled authentication)")
	}
	if a.Source == "auto" {
		a.Host = username
		a.Token = password
	}
	if err := a.Validate(); err == nil {
		arrValidated = true
	}

	if !arrValidated && cfg.UseAuth {
		// If arr validation failed, try to use user auth validation
		if !config.VerifyAuth(username, password) {
			return nil, fmt.Errorf("unauthorized: invalid credentials")
		}
	}
	if username != "" && password != "" {
		s.manager.Arr().AddOrUpdate(a)
	}
	return a, nil
}

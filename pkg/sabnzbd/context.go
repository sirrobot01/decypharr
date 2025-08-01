package sabnzbd

import (
	"context"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/store"
	"net/http"
	"strings"

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
		a := arr.New(category, "", "", false, false, &downloadUncached, "", "auto")

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
		host := r.FormValue("ma_username")
		token := r.FormValue("ma_password")
		category := getCategory(r.Context())
		arrs := store.Get().Arr()
		// Check if arr exists
		a := arrs.Get(category)
		if a == nil {
			// Arr is not configured, create a new one
			downloadUncached := false
			a = arr.New(category, "", "", false, false, &downloadUncached, "", "auto")
		}
		host = strings.TrimSpace(host)
		if host != "" {
			a.Host = host
		}
		token = strings.TrimSpace(token)
		if token != "" {
			a.Token = token
		}
		a.Source = "auto"
		if err := utils.ValidateServiceURL(a.Host); err != nil {
			// Return silently, no need to raise a problem. Just do not add the Arr to the context/config.json
			next.ServeHTTP(w, r)
			return
		}
		arrs.AddOrUpdate(a)
		ctx := context.WithValue(r.Context(), arrKey, a)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

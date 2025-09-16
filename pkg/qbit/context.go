package qbit

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/wire"
	"golang.org/x/crypto/bcrypt"
)

type contextKey string

const (
	categoryKey contextKey = "category"
	hashesKey   contextKey = "hashes"
	arrKey      contextKey = "arr"
)

func validateServiceURL(urlStr string) error {
	if urlStr == "" {
		return fmt.Errorf("URL cannot be empty")
	}

	// Try parsing as full URL first
	u, err := url.Parse(urlStr)
	if err == nil && u.Scheme != "" && u.Host != "" {
		// It's a full URL, validate scheme
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("URL scheme must be http or https")
		}
		return nil
	}

	// Check if it's a host:port format (no scheme)
	if strings.Contains(urlStr, ":") && !strings.Contains(urlStr, "://") {
		// Try parsing with http:// prefix
		testURL := "http://" + urlStr
		u, err := url.Parse(testURL)
		if err != nil {
			return fmt.Errorf("invalid host:port format: %w", err)
		}

		if u.Host == "" {
			return fmt.Errorf("host is required in host:port format")
		}

		// Validate port number
		if u.Port() == "" {
			return fmt.Errorf("port is required in host:port format")
		}

		return nil
	}

	return fmt.Errorf("invalid URL format: %s", urlStr)
}

func getCategory(ctx context.Context) string {
	if category, ok := ctx.Value(categoryKey).(string); ok {
		return category
	}
	return ""
}

func getHashes(ctx context.Context) []string {
	if hashes, ok := ctx.Value(hashesKey).([]string); ok {
		return hashes
	}
	return nil
}

func getArrFromContext(ctx context.Context) *arr.Arr {
	if a, ok := ctx.Value(arrKey).(*arr.Arr); ok {
		return a
	}
	return nil
}

func decodeAuthHeader(header string) (string, string, error) {
	encodedTokens := strings.Split(header, " ")
	if len(encodedTokens) != 2 {
		return "", "", nil
	}
	encodedToken := encodedTokens[1]

	bytes, err := base64.StdEncoding.DecodeString(encodedToken)
	if err != nil {
		return "", "", err
	}

	bearer := string(bytes)

	colonIndex := strings.LastIndex(bearer, ":")
	username := bearer[:colonIndex]
	password := bearer[colonIndex+1:]

	if username == "" || password == "" {
		return username, password, fmt.Errorf("empty username or password")
	}

	return strings.TrimSpace(username), strings.TrimSpace(password), nil
}

func (q *QBit) categoryContext(next http.Handler) http.Handler {
	// Print full URL for debugging

	// Try to get category from URL query first
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Print request method and URL
		category := strings.Trim(r.URL.Query().Get("category"), "")
		if category == "" {
			// Get from form
			_ = r.ParseForm()
			category = r.Form.Get("category")
			if category == "" {
				// Get from multipart form
				_ = r.ParseMultipartForm(32 << 20)
				category = r.FormValue("category")
			}
		}
		ctx := context.WithValue(r.Context(), categoryKey, strings.TrimSpace(category))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authContext creates a middleware that extracts the Arr host and token from the Authorization header
// and adds it to the request context.
// This is used to identify the Arr instance for the request.
// Only a valid host and token will be added to the context/config. The rest are manual
func (q *QBit) authContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		username, password, err := getUsernameAndPassword(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		category := getCategory(r.Context())
		a, err := q.authenticate(category, username, password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), arrKey, a)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func getUsernameAndPassword(r *http.Request) (string, string, error) {
	// Try to get from authorization header
	username, password, err := decodeAuthHeader(r.Header.Get("Authorization"))
	if err == nil && username != "" {
		return username, password, err
	}
	// Try to get from cookie
	sid, err := r.Cookie("sid")
	if err != nil {
		// try SID
		sid, err = r.Cookie("SID")
	}
	if err == nil {
		username, password, err = extractFromSID(sid.Value)
		if err != nil {
			return "", "", err
		}
	}
	return username, password, nil
}

func (q *QBit) authenticate(category, username, password string) (*arr.Arr, error) {
	cfg := config.Get()
	arrs := wire.Get().Arr()
	// Check if arr exists
	a := arrs.Get(category)
	if a == nil {
		// Arr is not configured, create a new one
		downloadUncached := false
		a = arr.New(category, "", "", false, false, &downloadUncached, "", "auto")
	}
	a.Host = username
	a.Token = password
	if cfg.UseAuth {
		if a.Host == "" || a.Token == "" {
			return nil, fmt.Errorf("unauthorized: Host and token are required for authentication(you've enabled authentication)")
		}
		// try to use either Arr validate, or user auth validation
		if err := a.Validate(); err != nil {
			// If this failed, try to use user auth validation
			if !verifyAuth(username, password) {
				return nil, fmt.Errorf("unauthorized: invalid credentials")
			}
		}
	}

	a.Source = "auto"
	arrs.AddOrUpdate(a)
	return a, nil
}

func createSID(username, password string) string {
	// Create a verification hash
	cfg := config.Get()
	combined := fmt.Sprintf("%s|%s", username, password)
	hash := sha256.Sum256([]byte(combined + cfg.SecretKey()))
	hashStr := fmt.Sprintf("%x", hash)[:16] // First 16 chars
	// Base64 encode
	return base64.URLEncoding.EncodeToString([]byte(fmt.Sprintf("%s|%s", combined, hashStr)))
}

func extractFromSID(sid string) (string, string, error) {
	// Decode base64
	decoded, err := base64.URLEncoding.DecodeString(sid)
	if err != nil {
		return "", "", fmt.Errorf("invalid SID format")
	}

	// Split into parts: username:password:hash
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 3 {
		return "", "", fmt.Errorf("invalid SID structure")
	}

	username := parts[0]
	password := parts[1]
	providedHash := parts[2]

	// Verify hash
	cfg := config.Get()
	combined := fmt.Sprintf("%s|%s", username, password)
	expectedHash := sha256.Sum256([]byte(combined + cfg.SecretKey()))
	expectedHashStr := fmt.Sprintf("%x", expectedHash)[:16]

	if providedHash != expectedHashStr {
		return "", "", fmt.Errorf("invalid SID signature")
	}

	return username, password, nil
}

func hashesContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_hashes := chi.URLParam(r, "hashes")
		var hashes []string
		if _hashes != "" {
			hashes = strings.Split(_hashes, "|")
		}
		if hashes == nil {
			// Get hashes from form
			_ = r.ParseForm()
			hashes = r.Form["hashes"]
		}
		for i, hash := range hashes {
			hashes[i] = strings.TrimSpace(hash)
		}
		ctx := context.WithValue(r.Context(), hashesKey, hashes)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func verifyAuth(username, password string) bool {
	// If you're storing hashed password, use bcrypt to compare
	if username == "" {
		return false
	}
	auth := config.Get().GetAuth()
	if auth == nil {
		return false
	}
	if username != auth.Username {
		return false
	}
	err := bcrypt.CompareHashAndPassword([]byte(auth.Password), []byte(password))
	return err == nil
}

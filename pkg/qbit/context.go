package qbit

import (
	"context"
	"encoding/base64"
	"github.com/go-chi/chi/v5"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/store"
	"net/http"
	"strings"
)

type contextKey string

const (
	categoryKey contextKey = "category"
	hashesKey   contextKey = "hashes"
	arrKey      contextKey = "arr"
)

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

func getArr(ctx context.Context) *arr.Arr {
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
	host := bearer[:colonIndex]
	token := bearer[colonIndex+1:]

	return host, token, nil
}

func (q *QBit) categoryContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func (q *QBit) authContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, token, err := decodeAuthHeader(r.Header.Get("Authorization"))
		category := getCategory(r.Context())
		arrs := store.Get().Arr()
		// Check if arr exists
		a := arrs.Get(category)
		if a == nil {
			downloadUncached := false
			a = arr.New(category, "", "", false, false, &downloadUncached, "")
		}
		if err == nil {
			host = strings.TrimSpace(host)
			if host != "" {
				a.Host = host
			}
			token = strings.TrimSpace(token)
			if token != "" {
				a.Token = token
			}
		}

		arrs.AddOrUpdate(a)
		ctx := context.WithValue(r.Context(), arrKey, a)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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

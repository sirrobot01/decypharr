package server

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestLayoutAssetVersions(t *testing.T) {
	templates := template.Must(template.New("").Funcs(template.FuncMap{
		"assetVersion": assetVersion,
	}).ParseGlob("templates/*.html"))
	staticFS, err := fs.Sub(assetsEmbed, "assets/build")
	if err != nil {
		t.Fatal(err)
	}
	assetURLs := regexp.MustCompile(`(?:src|href)="([^"]*/assets/[^"]+)"`)
	for _, base := range []string{"/", "/decypharr/"} {
		for _, page := range []string{"index", "browse", "download", "repair", "reacquire", "config", "stats", "login", "register", "setup"} {
			t.Run(base+page, func(t *testing.T) {
				layout := "layout"
				if page == "setup" {
					layout = "setup_layout"
				}
				var rendered bytes.Buffer
				if err := templates.ExecuteTemplate(&rendered, layout, map[string]any{
					"URLBase": base,
					"Page":    page,
				}); err != nil {
					t.Fatal(err)
				}
				matches := assetURLs.FindAllStringSubmatch(rendered.String(), -1)
				if len(matches) < 3 {
					t.Fatalf("expected CSS and JavaScript assets, got %d", len(matches))
				}
				for _, match := range matches {
					u, err := url.Parse(match[1])
					if err != nil {
						t.Fatal(err)
					}
					prefix := base + "assets/"
					if !strings.HasPrefix(u.Path, prefix) {
						t.Fatalf("asset %q does not respect URLBase %q", u.Path, base)
					}
					data, err := fs.ReadFile(staticFS, strings.TrimPrefix(u.Path, prefix))
					if err != nil {
						t.Fatal(err)
					}
					wantVersion := fmt.Sprintf("%x", sha256.Sum256(data))
					if got := u.Query().Get("v"); got != wantVersion {
						t.Fatalf("asset %s: version = %q, want %q", u.Path, got, wantVersion)
					}
					w := httptest.NewRecorder()
					http.StripPrefix(prefix, http.FileServer(http.FS(staticFS))).ServeHTTP(w, httptest.NewRequest(http.MethodGet, u.String(), nil))
					if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), data) {
						t.Fatalf("versioned asset %s: status %d or unexpected content", u, w.Code)
					}
				}
			})
		}
	}
}

func TestAssetVersionMissingFile(t *testing.T) {
	if _, err := assetVersion("js/missing.js"); err == nil {
		t.Fatal("expected an error for a missing asset")
	}
}

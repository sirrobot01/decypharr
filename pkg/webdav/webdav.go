package webdav

import (
	"context"
	"embed"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/store"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

//go:embed templates/*
var templatesFS embed.FS

var (
	funcMap = template.FuncMap{
		"add": func(a, b int) int {
			return a + b
		},
		"urlpath": func(p string) string {
			segments := strings.Split(p, "/")
			for i, segment := range segments {
				segments[i] = url.PathEscape(segment)
			}
			return strings.Join(segments, "/")
		},
		"formatSize": func(bytes int64) string {
			const (
				KB = 1024
				MB = 1024 * KB
				GB = 1024 * MB
				TB = 1024 * GB
			)

			var size float64
			var unit string

			switch {
			case bytes >= TB:
				size = float64(bytes) / TB
				unit = "TB"
			case bytes >= GB:
				size = float64(bytes) / GB
				unit = "GB"
			case bytes >= MB:
				size = float64(bytes) / MB
				unit = "MB"
			case bytes >= KB:
				size = float64(bytes) / KB
				unit = "KB"
			default:
				size = float64(bytes)
				unit = "bytes"
			}

			// Format to 2 decimal places for larger units, no decimals for bytes
			if unit == "bytes" {
				return fmt.Sprintf("%.0f %s", size, unit)
			}
			return fmt.Sprintf("%.2f %s", size, unit)
		},
		"hasSuffix": strings.HasSuffix,
	}
	tplRoot      = template.Must(template.ParseFS(templatesFS, "templates/root.html"))
	tplDirectory = template.Must(template.New("").Funcs(funcMap).ParseFS(templatesFS, "templates/directory.html"))
)

func init() {
	chi.RegisterMethod("PROPFIND")
	chi.RegisterMethod("PROPPATCH")
	chi.RegisterMethod("MKCOL")
	chi.RegisterMethod("COPY")
	chi.RegisterMethod("MOVE")
	chi.RegisterMethod("LOCK")
	chi.RegisterMethod("UNLOCK")
}

type WebDav struct {
	Handlers []*Handler
	URLBase  string
}

func New() *WebDav {
	cfg := config.Get()
	urlBase := cfg.URLBase

	w := &WebDav{
		Handlers: make([]*Handler, 0),
		URLBase:  urlBase,
	}
	for name, c := range store.Get().Debrid().Caches() {
		h := NewHandler(name, urlBase, c, c.Logger())
		w.Handlers = append(w.Handlers, h)
	}
	return w
}

func (wd *WebDav) Routes() http.Handler {
	wr := chi.NewRouter()
	wr.Use(middleware.StripSlashes)
	wr.Use(wd.commonMiddleware)

	wd.setupRootHandler(wr)
	wd.mountHandlers(wr)

	return wr
}

func (wd *WebDav) Start(ctx context.Context) error {
	wg := sync.WaitGroup{}
	errChan := make(chan error, len(wd.Handlers))

	for _, h := range wd.Handlers {
		wg.Add(1)
		go func(h *Handler) {
			defer wg.Done()
			if err := h.cache.Start(ctx); err != nil {
				select {
				case errChan <- err:
				default:
				}
			}
		}(h)
	}

	// Use a separate goroutine to close channel after WaitGroup
	go func() {
		wg.Wait()
		close(errChan)
	}()

	// Collect all errors
	var errors []error
	for err := range errChan {
		if err != nil {
			errors = append(errors, err)
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("multiple handlers failed: %v", errors)
	}
	return nil
}

func (wd *WebDav) mountHandlers(r chi.Router) {
	for _, h := range wd.Handlers {
		r.Route("/"+h.Name, func(r chi.Router) {
			r.Use(h.readinessMiddleware)
			r.Mount("/", h)
		}) // Mount to /name since router is already prefixed with /webdav
	}
}

func (wd *WebDav) setupRootHandler(r chi.Router) {
	r.Get("/", wd.handleGetRoot())
	r.MethodFunc("PROPFIND", "/", wd.handleWebdavRoot())
}

func (wd *WebDav) commonMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("DAV", "1, 2")
		w.Header().Set("Allow", "OPTIONS, PROPFIND, GET, HEAD, POST, PUT, DELETE, MKCOL, PROPPATCH, COPY, MOVE, LOCK, UNLOCK")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "OPTIONS, PROPFIND, GET, HEAD, POST, PUT, DELETE, MKCOL, PROPPATCH, COPY, MOVE, LOCK, UNLOCK")
		w.Header().Set("Access-Control-Allow-Headers", "Depth, Content-Type, Authorization")

		next.ServeHTTP(w, r)
	})
}

func (wd *WebDav) handleGetRoot() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		data := struct {
			Handlers []*Handler
			URLBase  string
		}{
			Handlers: wd.Handlers,
			URLBase:  wd.URLBase,
		}
		if err := tplRoot.Execute(w, data); err != nil {
			return
		}
	}
}

func (wd *WebDav) handleWebdavRoot() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fi := &FileInfo{
			name:    "/",
			size:    0,
			mode:    0755 | os.ModeDir,
			modTime: time.Now(),
			isDir:   true,
		}
		children := make([]os.FileInfo, 0, len(wd.Handlers))
		for _, h := range wd.Handlers {
			children = append(children, &FileInfo{
				name:    h.Name,
				size:    0,
				mode:    0755 | os.ModeDir,
				modTime: time.Now(),
				isDir:   true,
			})
		}
		sb := filesToXML(path.Clean(r.URL.Path), fi, children)
		writeXml(w, http.StatusMultiStatus, sb)

	}
}

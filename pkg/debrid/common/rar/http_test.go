package rar

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPFileReadAt(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		body         string
		size, offset int64
		count        int
		want         string
		wantErr      error
	}{
		{"range", 206, "bcd", 6, 1, 3, "bcd", nil},
		{"range at end", 206, "ef", 6, 4, 4, "ef", io.EOF},
		{"truncated range", 206, "bc", 6, 1, 3, "bc", io.ErrUnexpectedEOF},
		{"ignored range", 200, "abcdef", 6, 1, 3, "bcd", nil},
		{"ignored range at end", 200, "abcdef", 6, 4, 4, "ef", io.EOF},
		{"truncated full response", 200, "ab", 6, 1, 3, "b", io.ErrUnexpectedEOF},
		{"range rejected", 416, "", 6, 1, 3, "", io.EOF},
		{"past end", 200, "abcdef", 6, 7, 1, "", io.EOF},
		{"empty read", 200, "abcdef", 6, 6, 0, "", nil},
		{"negative offset", 200, "abcdef", 6, -1, 1, "", fs.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			f := &HttpFile{URL: srv.URL, client: srv.Client(), FileSize: tc.size}
			buf := make([]byte, tc.count)
			n, err := f.ReadAt(buf, tc.offset)
			if !errors.Is(err, tc.wantErr) || string(buf[:n]) != tc.want {
				t.Fatalf("ReadAt = %q, %v; want %q, %v", buf[:n], err, tc.want, tc.wantErr)
			}
			if n < len(buf) && err == nil {
				t.Fatal("short read returned nil error")
			}
		})
	}
}

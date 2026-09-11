package request

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "github.com/bytedance/sonic"
)

// errAny marks a case that wants any error, not a specific one.
var errAny = errors.New("any error")

type record struct {
	ID   int    `json:"id"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func TestDecodeJSON(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		chunked bool
		want    int
		wantErr error
	}{
		{name: "sized", body: `[{"id":1},{"id":2}]`, want: 2},
		{name: "chunked", body: `[{"id":1},{"id":2}]`, chunked: true, want: 2},
		// An empty body reports io.EOF, the same as a streaming decoder, so
		// call sites that treated it as an error still do.
		{name: "empty body reports EOF", body: "", wantErr: io.EOF},
		{name: "malformed", body: `[{"id":`, wantErr: errAny},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if !test.chunked {
					w.Header().Set("Content-Length", fmt.Sprint(len(test.body)))
				}
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()

			resp, err := http.Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer DrainAndCloseResponse(resp)

			var out []record
			err = DecodeJSON(resp, &out)
			if test.wantErr != nil {
				if err == nil {
					t.Fatal("want an error")
				}
				if test.wantErr != errAny && !errors.Is(err, test.wantErr) {
					t.Fatalf("err = %v, want %v", err, test.wantErr)
				}
				if len(out) != 0 {
					t.Fatalf("out = %#v, want untouched", out)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != test.want {
				t.Fatalf("out = %#v", out)
			}
		})
	}
}

func TestDecodeJSONIgnoresNilInputs(t *testing.T) {
	if err := DecodeJSON(nil, &[]record{}); err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(`[{"id":1}]`))}
	if err := DecodeJSON(resp, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeJSONRejectsTrailingValue(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(`[{"id":1}] {"unexpected":true}`))}
	var out []record
	if err := DecodeJSON(resp, &out); err == nil {
		t.Fatal("want an error for a second JSON value")
	}
}

func TestDecodeJSONArrayStreamsItems(t *testing.T) {
	data := payload(t, 1000)
	stream := &body{data: data, chunk: 128}
	resp := &http.Response{Body: stream}

	count, bytesReadAtFirstVisit := 0, 0
	err := DecodeJSONArray[record](resp, func(item record) error {
		if item.ID <= 0 {
			t.Fatalf("item = %#v", item)
		}
		count++
		if count == 1 {
			bytesReadAtFirstVisit = stream.off
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1000 {
		t.Fatalf("visited %d items, want 1000", count)
	}
	if bytesReadAtFirstVisit <= 0 || bytesReadAtFirstVisit >= len(data) {
		t.Fatalf("first item visited after reading %d of %d bytes", bytesReadAtFirstVisit, len(data))
	}
}

func TestDecodeJSONArrayRejectsInvalidEnvelope(t *testing.T) {
	for _, body := range []string{
		`{"id":1}`,
		`[{"id":1}`,
		`[{"id":1}] {"unexpected":true}`,
	} {
		resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
		if err := DecodeJSONArray[record](resp, func(record) error { return nil }); err == nil {
			t.Fatalf("body %q: want an error", body)
		}
	}
}

func TestDecodeJSONArrayReturnsVisitorError(t *testing.T) {
	want := errors.New("stop visiting")
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(`[{"id":1},{"id":2}]`))}
	err := DecodeJSONArray[record](resp, func(record) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestDecodeJSONArrayAcceptsNull(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(`null`))}
	if err := DecodeJSONArray[record](resp, func(record) error {
		t.Fatal("visited an item for a null array")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// body serves a payload in bounded reads, the way an HTTP body arrives off the
// wire. Buffer growth is driven by the read size, not the payload size.
type body struct {
	data  []byte
	off   int
	chunk int
}

func (b *body) Read(p []byte) (int, error) {
	if b.off >= len(b.data) {
		return 0, io.EOF
	}
	n := min(min(len(p), b.chunk), len(b.data)-b.off)
	copy(p, b.data[b.off:b.off+n])
	b.off += n
	return n, nil
}

func (b *body) Close() error { return nil }

func payload(tb testing.TB, records int) []byte {
	rows := make([]record, records)
	for i := range rows {
		rows[i] = record{
			ID:   i + 1,
			Path: fmt.Sprintf("/mnt/decypharr/tv/A Show (2019)/Season %02d/A Show (2019) - S%02dE%02d - Title [Bluray-1080p][x264]-GROUP.mkv", i%20+1, i%20+1, i%24+1),
			Size: 3_500_000_000,
		}
	}
	data, err := json.Marshal(rows)
	if err != nil {
		tb.Fatal(err)
	}
	return data
}

// BenchmarkDecodeJSON measures callers that deliberately materialize a whole
// response. Large Arr collection endpoints use BenchmarkDecodeJSONArray's
// bounded streaming path instead.
func BenchmarkDecodeJSON(b *testing.B) {
	for _, records := range []int{500, 5000, 20000} {
		data := payload(b, records)
		b.Run(fmt.Sprintf("records=%d/bytes=%d", records, len(data)), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				resp := &http.Response{
					ContentLength: int64(len(data)),
					Body:          &body{data: data, chunk: 16 << 10},
				}
				var out []record
				if err := DecodeJSON(resp, &out); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDecodeJSONArray(b *testing.B) {
	for _, records := range []int{500, 5000, 20000} {
		data := payload(b, records)
		b.Run(fmt.Sprintf("records=%d/bytes=%d", records, len(data)), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				resp := &http.Response{
					ContentLength: int64(len(data)),
					Body:          &body{data: data, chunk: 16 << 10},
				}
				count := 0
				if err := DecodeJSONArray[record](resp, func(record) error {
					count++
					return nil
				}); err != nil {
					b.Fatal(err)
				}
				if count != records {
					b.Fatalf("visited %d records, want %d", count, records)
				}
			}
		})
	}
}

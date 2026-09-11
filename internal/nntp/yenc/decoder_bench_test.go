package yenc

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// chunkedReader returns network-sized chunks (one TLS record's worth) per
// Read, matching what the connection's bufio typically hands the decoder.
type chunkedReader struct {
	reader strings.Reader
	limit  int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.limit > 0 && len(p) > r.limit {
		p = p[:r.limit]
	}
	return r.reader.Read(p)
}

func BenchmarkBodyDecoderNext(b *testing.B) {
	decoded := make([]byte, 750<<10)
	for i := range decoded {
		decoded[i] = byte(i % 251)
	}
	response := bodyResponse(yencEncode(decoded, "bench.bin", 1, 1, int64(len(decoded))))

	pool := sync.Pool{New: func() any { b := make([]byte, 0, 1<<20); return &b }}
	dataFunc := func() []byte { return *pool.Get().(*[]byte) }

	for _, source := range []struct {
		name  string
		limit int
	}{
		{name: "Memory", limit: 0},
		{name: "Chunked16K", limit: 16 << 10},
	} {
		b.Run(source.name, func(b *testing.B) {
			// One decoder reused across articles, as a connection holds it.
			reader := &chunkedReader{limit: source.limit}
			dec := NewBodyDecoder(reader, dataFunc)

			// Verify before timing.
			reader.reader.Reset(response)
			res, err := dec.Next()
			if err != nil {
				b.Fatal(err)
			}
			if !bytes.Equal(res.Data, decoded) {
				b.Fatalf("decoded %d bytes, want %d", len(res.Data), len(decoded))
			}
			pool.Put(&res.Data)

			b.SetBytes(int64(len(decoded)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				reader.reader.Reset(response)
				res, err := dec.Next()
				if err != nil {
					b.Fatal(err)
				}
				if len(res.Data) != len(decoded) {
					b.Fatalf("decoded %d bytes, want %d", len(res.Data), len(decoded))
				}
				pool.Put(&res.Data)
			}
		})
	}
}

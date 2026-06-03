//go:build cgo

package yenc

import (
	"io"
	"sync"

	"github.com/Tensai75/rapidyenc"
)

// rapidyencAdapter wraps a rapidyenc.Decoder to sync Meta after each Read.
type rapidyencAdapter struct {
	dec     *rapidyenc.Decoder
	yencDec *Decoder
}

var rapidyencAdapterPool = sync.Pool{
	New: func() any {
		yd := &Decoder{}
		return &rapidyencAdapter{yencDec: yd}
	},
}

func (a *rapidyencAdapter) Read(p []byte) (int, error) {
	n, err := a.dec.Read(p)
	// Sync meta from rapidyenc after each read (headers parsed lazily)
	m := a.dec.Meta
	a.yencDec.Meta = DecoderMeta{
		FileName:   m.FileName,
		FileSize:   m.FileSize,
		PartNumber: m.PartNumber,
		TotalParts: m.TotalParts,
		Offset:     m.Offset,
		PartSize:   m.PartSize,
	}
	return n, err
}

// AcquireDecoder returns a Decoder backed by rapidyenc (CGO).
func AcquireDecoder(r io.Reader) *Decoder {
	if UsePureGo {
		return acquirePureGoDecoder(r)
	}
	adapter := rapidyencAdapterPool.Get().(*rapidyencAdapter)
	adapter.dec = rapidyenc.AcquireDecoder(r)
	adapter.yencDec.Reader = adapter
	adapter.yencDec.Meta = DecoderMeta{}
	return adapter.yencDec
}

// ReleaseDecoder returns the underlying rapidyenc decoder to the pool.
func ReleaseDecoder(dec *Decoder) {
	if dec == nil {
		return
	}
	if adapter, ok := dec.Reader.(*rapidyencAdapter); ok {
		rapidyenc.ReleaseDecoder(adapter.dec)
		adapter.dec = nil
		dec.Reader = nil
		dec.Meta = DecoderMeta{}
		rapidyencAdapterPool.Put(adapter)
	}
}

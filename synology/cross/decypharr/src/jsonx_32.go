//go:build 386 || arm

package jsonx

import (
	stdjson "encoding/json"
	"io"
)

type RawMessage = stdjson.RawMessage

var (
	Marshal       = stdjson.Marshal
	Unmarshal     = stdjson.Unmarshal
	MarshalIndent = stdjson.MarshalIndent
	Valid         = stdjson.Valid
)

type defaultAPI struct{}

var ConfigDefault defaultAPI

func (defaultAPI) NewDecoder(r io.Reader) *stdjson.Decoder {
	return stdjson.NewDecoder(r)
}

func (defaultAPI) NewEncoder(w io.Writer) *stdjson.Encoder {
	return stdjson.NewEncoder(w)
}

func NewDecoder(r io.Reader) *stdjson.Decoder {
	return stdjson.NewDecoder(r)
}

func NewEncoder(w io.Writer) *stdjson.Encoder {
	return stdjson.NewEncoder(w)
}

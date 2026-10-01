//go:build amd64 || arm64

package jsonx

import (
	stdjson "encoding/json"
	"io"

	sonic "github.com/bytedance/sonic"
)

type RawMessage = stdjson.RawMessage

var (
	Marshal       = sonic.Marshal
	Unmarshal     = sonic.Unmarshal
	MarshalIndent = stdjson.MarshalIndent
	Valid         = sonic.Valid
)

var ConfigDefault = sonic.ConfigDefault

func NewDecoder(r io.Reader) interface{ Decode(any) error } {
	return sonic.ConfigDefault.NewDecoder(r)
}

func NewEncoder(w io.Writer) interface{ Encode(any) error } {
	return sonic.ConfigDefault.NewEncoder(w)
}

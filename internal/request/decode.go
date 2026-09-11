package request

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// DecodeJSON decodes one JSON value directly from a response body.
//
// The standard-library decoder owns decoded strings instead of leaving them
// backed by the response document. That ownership boundary matters for large
// Arr responses: a path retained by an index must not keep the entire JSON
// document live. An empty body returns io.EOF and leaves out untouched.
func DecodeJSON(resp *http.Response, out any) error {
	if resp == nil || resp.Body == nil || out == nil {
		return nil
	}
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(out); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

// DecodeJSONArray streams a top-level JSON array and visits one decoded item
// at a time. Memory is therefore bounded by the largest item plus values the
// visitor deliberately retains, rather than by the size of the whole array.
func DecodeJSONArray[T any](resp *http.Response, visit func(T) error) error {
	if resp == nil || resp.Body == nil || visit == nil {
		return nil
	}

	decoder := json.NewDecoder(resp.Body)
	opening, err := decoder.Token()
	if err != nil {
		return err
	}
	if opening == nil {
		return requireJSONEOF(decoder)
	}
	if opening != json.Delim('[') {
		return fmt.Errorf("expected JSON array, got %v", opening)
	}

	for decoder.More() {
		var item T
		if err := decoder.Decode(&item); err != nil {
			return err
		}
		if err := visit(item); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	if closing != json.Delim(']') {
		return fmt.Errorf("expected end of JSON array, got %v", closing)
	}
	return requireJSONEOF(decoder)
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return errors.New("response body contains more than one JSON value")
}

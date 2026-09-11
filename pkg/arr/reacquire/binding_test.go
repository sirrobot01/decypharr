package reacquire

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/sirrobot01/decypharr/pkg/arr"
)

func TestBindingsFromMatchesOwnLibraryPath(t *testing.T) {
	const suffix = "/library/movie.mkv"
	responseDocument := strings.Repeat("x", 1<<20) + suffix
	path := responseDocument[len(responseDocument)-len(suffix):]

	bindings := bindingsFromMatches(arr.Arr{}, 1, []libraryMatch{{library: arr.LibraryFile{Path: path}}})
	if len(bindings) != 1 || bindings[0].LibraryPath != suffix {
		t.Fatalf("bindings = %#v, want library path %q", bindings, suffix)
	}
	if unsafe.StringData(bindings[0].LibraryPath) == unsafe.StringData(path) {
		t.Fatal("binding still shares the response document backing storage")
	}
}

func TestBindingRequiresCurrentIdentityToAuthorizeMutation(t *testing.T) {
	binding := Binding{
		ArrType:                arr.Radarr,
		ArrInstanceFingerprint: "v1:test",
		ArrFileID:              42,
		LibraryPath:            "/library/movie.mkv",
		Confidence:             ConfidenceExactPath,
	}
	if !binding.AuthorizesMutation() {
		t.Fatal("current binding did not authorize mutation")
	}

	binding.ArrInstanceFingerprint = ""
	if binding.AuthorizesMutation() {
		t.Fatal("legacy binding without an instance fingerprint authorized mutation")
	}
	binding.ArrInstanceFingerprint = "v1:test"
	binding.LibraryPath = ""
	if binding.AuthorizesMutation() {
		t.Fatal("binding without a library path authorized mutation")
	}
}

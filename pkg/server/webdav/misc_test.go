package webdav

import (
	"strings"
	"testing"

	"github.com/sirrobot01/decypharr/pkg/manager"
)

// RFC 4918 defines getlastmodified as an rfc1123-date.
func TestConvertToXMLUsesHTTPDate(t *testing.T) {
	sb := convertToXML("/", &manager.FileInfo{}, nil)
	want := "<d:getlastmodified>Mon, 01 Jan 0001 00:00:00 GMT</d:getlastmodified>"
	if got := sb.String(); !strings.Contains(got, want) {
		t.Fatalf("PROPFIND body = %s, want it to contain %s", got, want)
	}
}

package link

import "testing"

// Transient provider codes must land in the Refetchable category: it is the only
// one the validation path acts on, so it is the only one that drops the memoised
// failure and asks for a fresh link. Anything else leaves the entry cached and
// the file unreadable until the process restarts.
//
// The second half guards against over-correcting: genuinely permanent codes must
// stay permanent, otherwise the caller would refetch forever on a dead file.
func TestTransientCodesAreRefetchable(t *testing.T) {
	transient := []string{"400", "429", "500", "502", "503", "504", "some_unrecognised_code"}
	for _, code := range transient {
		e := ErrorCodeToLinkError(code)
		if e.IsPermanent() {
			t.Errorf("code %q classified as permanent: the file would stay unreadable until restart", code)
		}
		if !e.ShouldRefetch() {
			t.Errorf("code %q: ShouldRefetch() is false, so the memoised failure is never cleared", code)
		}
	}

	permanent := []string{"401", "unauthorized", "404", "link_not_found", "file_not_available"}
	for _, code := range permanent {
		e := ErrorCodeToLinkError(code)
		if !e.IsPermanent() {
			t.Errorf("code %q should remain permanent", code)
		}
		if e.ShouldRefetch() {
			t.Errorf("code %q should not trigger a refetch", code)
		}
	}
}

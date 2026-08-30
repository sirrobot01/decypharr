package request

import (
	"io"
	"net/http"
)

// maxDrain bounds how much of an unread body is consumed before the connection
// is given up on. API responses are far smaller than this.
const maxDrain = 64 << 10

// DrainAndClose consumes the unread remainder of an API response body so the
// connection returns to the idle pool, then closes it. net/http only reuses a
// connection whose body was read to EOF; closing a partially-read body makes it
// discard the connection instead, which shows up as connection churn under load.
//
// Only for small responses. Never call it on a download or streaming body — it
// would read up to maxDrain bytes of payload for nothing.
func DrainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxDrain))
	_ = body.Close()
}

// DrainAndCloseResponse is the http.Response form of DrainAndClose, for the
// common `defer request.DrainAndCloseResponse(resp)` after a nil-error Do.
func DrainAndCloseResponse(resp *http.Response) {
	if resp == nil {
		return
	}
	DrainAndClose(resp.Body)
}

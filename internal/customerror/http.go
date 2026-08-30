package customerror

import "errors"

var HosterUnavailableError = (&Error{
	statusCode: 503,
	err:        errors.New("hoster is unavailable"),
	Code:       "hoster_unavailable",
}).Retryable() // 503 Service Unavailable is transient

var UsenetSegmentMissingError = &Error{
	statusCode: 404,
	err:        errors.New("usenet segment is missing"),
	Code:       "usenet_segment_missing",
}

var UsenetCorruptContentError = &Error{
	statusCode: 422,
	err:        errors.New("usenet file content is corrupt"),
	Code:       "usenet_corrupt_content",
}

var TrafficExceededError = &Error{
	statusCode: 503,
	err:        errors.New("traffic limit exceeded"),
	Code:       "traffic_exceeded",
}

var TorrentNotFoundError = &Error{
	statusCode: 404,
	err:        errors.New("torrent not found"),
	Code:       "torrent_not_found",
}

var TooManyActiveDownloadsError = (&Error{
	statusCode: 509,
	err:        errors.New("too many active downloads"),
	Code:       "too_many_active_downloads",
}).Retryable() // slot exhaustion is transient — retry after backoff

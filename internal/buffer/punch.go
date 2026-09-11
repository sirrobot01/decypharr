package buffer

import "errors"

// errPunchUnsupported means the filesystem cannot release blocks from the
// middle of a file. Callers must leave the range published: the bytes are
// still there, and reporting them evicted only costs a re-download.
var errPunchUnsupported = errors.New("buffer: hole punching unsupported")

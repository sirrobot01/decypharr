package nntp

import (
	nntpyenc "github.com/sirrobot01/decypharr/internal/nntp/yenc"
)

type YencMetadata = nntpyenc.Metadata

// Segment represents a usenet segment
type Segment struct {
	MessageID string
	Number    int
	Bytes     int64
	Data      []byte
}

// Article represents a complete usenet article
type Article struct {
	MessageID string
	Subject   string
	From      string
	Date      string
	Groups    []string
	Body      []byte
	Size      int64
}

// Response represents an NNTP server response
type Response struct {
	Code    int
	Message string
	Lines   []string
}

// GroupInfo represents information about a newsgroup
type GroupInfo struct {
	Name  string
	Count int // Number of articles in the group
	Low   int // Lowest article number
	High  int // Highest article number
}

// StatResult represents the result of a STAT command for a single message ID
type StatResult struct {
	MessageID string // The message ID that was checked
	Available bool   // Whether the article is available
	Error     error  // Error if any (nil means success or article found)
}

// BatchStatResult contains results for all message IDs in a batch
type BatchStatResult struct {
	Results    []StatResult // Per-message results
	TotalCount int          // Total number of messages checked
	FoundCount int          // Number of messages found
	ErrorCount int          // Number of errors (excluding not found)
}

// HasErrors returns true if any non-ArticleNotFound errors occurred
func (r *BatchStatResult) HasErrors() bool {
	return r.ErrorCount > 0
}

// AllAvailable returns true if all messages are available
func (r *BatchStatResult) AllAvailable() bool {
	return r.FoundCount == r.TotalCount
}

// FirstError returns the first error encountered, or nil if none
func (r *BatchStatResult) FirstError() error {
	for _, res := range r.Results {
		if res.Error != nil {
			return res.Error
		}
	}
	return nil
}

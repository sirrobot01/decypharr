package webdav

import (
	"os"
	"time"
)

// FileInfo implements os.FileInfo for our WebDAV files
type FileInfo struct {
	id      string
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (fi *FileInfo) Name() string       { return fi.name } // uses minimal escaping
func (fi *FileInfo) Size() int64        { return fi.size }
func (fi *FileInfo) Mode() os.FileMode  { return fi.mode }
func (fi *FileInfo) ModTime() time.Time { return fi.modTime }
func (fi *FileInfo) IsDir() bool        { return fi.isDir }
func (fi *FileInfo) ID() string         { return fi.id }
func (fi *FileInfo) Sys() interface{}   { return nil }

package webdav

type File interface {
	Name() string
	Size() int64
	IsDir() bool
	ModTime() string
}

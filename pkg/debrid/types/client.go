package types

import (
	"github.com/rs/zerolog"
)

type Client interface {
	SubmitMagnet(tr *Torrent) (*Torrent, error)
	CheckStatus(tr *Torrent) (*Torrent, error)
	GetFileDownloadLinks(tr *Torrent) error
	GetDownloadLink(tr *Torrent, file *File) (*DownloadLink, error)
	DeleteTorrent(torrentId string) error
	IsAvailable(infohashes []string) map[string]bool
	GetDownloadUncached() bool
	UpdateTorrent(torrent *Torrent) error
	GetTorrent(torrentId string) (*Torrent, error)
	GetTorrents() ([]*Torrent, error)
	Name() string
	Logger() zerolog.Logger
	GetDownloadingStatus() []string
	GetDownloadLinks() (map[string]*DownloadLink, error)
	CheckLink(link string) error
	GetMountPath() string
	Accounts() *Accounts // Returns the active download account/token
	DeleteDownloadLink(linkId string) error
	GetProfile() (*Profile, error)
	GetAvailableSlots() (int, error)
	SyncAccounts() error // Updates each accounts details(like traffic, username, etc.)
}

package common

import (
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

type Client interface {
	SubmitMagnet(tr *types.Torrent) (*types.Torrent, error)
	CheckStatus(tr *types.Torrent) (*types.Torrent, error)
	GetFileDownloadLinks(tr *types.Torrent) error
	GetDownloadLink(tr *types.Torrent, file *types.File) (types.DownloadLink, error)
	DeleteTorrent(torrentId string) error
	IsAvailable(infohashes []string) map[string]bool
	GetDownloadUncached() bool
	UpdateTorrent(torrent *types.Torrent) error
	GetTorrent(torrentId string) (*types.Torrent, error)
	GetTorrents() ([]*types.Torrent, error)
	Name() string
	Logger() zerolog.Logger
	GetDownloadingStatus() []string
	RefreshDownloadLinks() error
	CheckLink(link string) error
	GetMountPath() string
	AccountManager() *account.Manager // Returns the active download account/token
	GetProfile() (*types.Profile, error)
	GetAvailableSlots() (int, error)
	SyncAccounts() error // Updates each accounts details(like traffic, username, etc.)
}

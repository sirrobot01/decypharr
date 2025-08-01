package sabnzbd

import (
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/store"
	"github.com/sirrobot01/decypharr/pkg/usenet"
	"path/filepath"
)

type SABnzbd struct {
	downloadFolder    string
	config            *Config
	refreshInterval   int
	logger            zerolog.Logger
	usenet            usenet.Usenet
	defaultCategories []string
}

func New(usenetClient usenet.Usenet) *SABnzbd {
	_cfg := config.Get()
	cfg := _cfg.SABnzbd
	var defaultCategories []string
	for _, cat := range _cfg.SABnzbd.Categories {
		if cat != "" {
			defaultCategories = append(defaultCategories, cat)
		}
	}
	sb := &SABnzbd{
		downloadFolder:    cfg.DownloadFolder,
		refreshInterval:   cfg.RefreshInterval,
		logger:            logger.New("sabnzbd"),
		usenet:            usenetClient,
		defaultCategories: defaultCategories,
	}
	sb.SetConfig(_cfg)
	return sb
}

func (s *SABnzbd) SetConfig(cfg *config.Config) {
	sabnzbdConfig := &Config{
		Misc: MiscConfig{
			CompleteDir:   s.downloadFolder,
			DownloadDir:   s.downloadFolder,
			AdminDir:      s.downloadFolder,
			WebPort:       cfg.Port,
			Language:      "en",
			RefreshRate:   "1",
			QueueComplete: "0",
			ConfigLock:    "0",
			Autobrowser:   "1",
			CheckNewRel:   "1",
		},
		Categories: s.getCategories(),
	}
	if cfg.Usenet != nil || len(cfg.Usenet.Providers) == 0 {
		for _, provider := range cfg.Usenet.Providers {
			if provider.Host == "" || provider.Port == 0 {
				continue
			}
			sabnzbdConfig.Servers = append(sabnzbdConfig.Servers, Server{
				Name:        provider.Name,
				Host:        provider.Host,
				Port:        provider.Port,
				Username:    provider.Username,
				Password:    provider.Password,
				Connections: provider.Connections,
				SSL:         provider.SSL,
			})
		}
	}
	s.config = sabnzbdConfig
}

func (s *SABnzbd) getCategories() []Category {
	_store := store.Get()
	arrs := _store.Arr().GetAll()
	categories := make([]Category, 0, len(arrs))
	added := map[string]struct{}{}

	for i, a := range arrs {
		if _, ok := added[a.Name]; ok {
			continue // Skip if category already added
		}
		categories = append(categories, Category{
			Name:     a.Name,
			Order:    i + 1,
			Pp:       "3",
			Script:   "None",
			Dir:      filepath.Join(s.downloadFolder, a.Name),
			Priority: PriorityNormal,
		})
	}

	// Add default categories if not already present
	for _, defaultCat := range s.defaultCategories {
		if _, ok := added[defaultCat]; ok {
			continue // Skip if default category already added
		}
		categories = append(categories, Category{
			Name:     defaultCat,
			Order:    len(categories) + 1,
			Pp:       "3",
			Script:   "None",
			Dir:      filepath.Join(s.downloadFolder, defaultCat),
			Priority: PriorityNormal,
		})
		added[defaultCat] = struct{}{}
	}

	return categories
}

func (s *SABnzbd) Reset() {
}

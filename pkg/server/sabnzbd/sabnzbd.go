package sabnzbd

import (
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/manager"
)

type SABnzbd struct {
	downloadFolder    string
	refreshInterval   time.Duration
	logger            zerolog.Logger
	manager           *manager.Manager
	defaultCategories []string
	config            *Config
}

func New(manager *manager.Manager) *SABnzbd {
	cfg := config.Get()
	var defaultCategories []string
	for _, cat := range cfg.Categories {
		if cat != "" {
			defaultCategories = append(defaultCategories, cat)
		}
	}
	refreshInterval, err := utils.ParseDuration(cfg.RefreshInterval)
	if err != nil {
		refreshInterval = 30 * time.Second
	}
	sb := &SABnzbd{
		downloadFolder:    cfg.DownloadFolder,
		refreshInterval:   refreshInterval,
		logger:            logger.New("sabnzbd"),
		defaultCategories: defaultCategories,
		manager:           manager,
	}
	sb.SetConfig(cfg)
	return sb
}

func (s *SABnzbd) SetConfig(cfg *config.Config) {
	sabnzbdConfig := &Config{
		Misc: MiscConfig{
			CompleteDir:            s.downloadFolder,
			DownloadDir:            s.downloadFolder,
			AdminDir:               s.downloadFolder,
			WebPort:                cfg.Port,
			Language:               "en",
			RefreshRate:            "1",
			QueueComplete:          "0",
			ConfigLock:             "0",
			Autobrowser:            "1",
			CheckNewRel:            "1",
			SeasonPackRenaming: boolToString(cfg.Usenet.SeasonPackRenaming),
		},
		Categories: s.getCategories(),
	}
	if len(cfg.Usenet.Providers) > 0 {
		for _, provider := range cfg.Usenet.Providers {
			if provider.Host == "" || provider.Port == 0 {
				continue
			}
			sabnzbdConfig.Servers = append(sabnzbdConfig.Servers, Server{
				Name:        provider.Host,
				Host:        provider.Host,
				Port:        provider.Port,
				Username:    provider.Username,
				Password:    provider.Password,
				Connections: provider.MaxConnections,
				SSL:         provider.SSL,
			})
		}
	}
	s.config = sabnzbdConfig
}

func (s *SABnzbd) getCategories() []Category {
	arrs := s.manager.Arr().GetAll()
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

func boolToString(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

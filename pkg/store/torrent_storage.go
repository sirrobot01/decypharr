package store

import (
	"encoding/json"
	"fmt"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"os"
	"sort"
	"sync"
)

func keyPair(hash, category string) string {
	return fmt.Sprintf("%s|%s", hash, category)
}

type Torrents = map[string]*Torrent

type TorrentStorage struct {
	torrents Torrents
	mu       sync.RWMutex
	filename string // Added to store the filename for persistence
}

func loadTorrentsFromJSON(filename string) (Torrents, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	torrents := make(Torrents)
	if err := json.Unmarshal(data, &torrents); err != nil {
		return nil, err
	}
	return torrents, nil
}

func newTorrentStorage(filename string) *TorrentStorage {
	// Open the JSON file and read the data
	torrents, err := loadTorrentsFromJSON(filename)
	if err != nil {
		torrents = make(Torrents)
	}
	// Create a new Storage
	return &TorrentStorage{
		torrents: torrents,
		filename: filename,
	}
}

func (ts *TorrentStorage) Add(torrent *Torrent) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.torrents[keyPair(torrent.Hash, torrent.Category)] = torrent
	go func() {
		err := ts.saveToFile()
		if err != nil {
			fmt.Println(err)
		}
	}()
}

func (ts *TorrentStorage) AddOrUpdate(torrent *Torrent) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.torrents[keyPair(torrent.Hash, torrent.Category)] = torrent
	go func() {
		err := ts.saveToFile()
		if err != nil {
			fmt.Println(err)
		}
	}()
}

func (ts *TorrentStorage) Get(hash, category string) *Torrent {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	torrent, exists := ts.torrents[keyPair(hash, category)]
	if !exists && category == "" {
		// Try to find the torrent without knowing the category
		for _, t := range ts.torrents {
			if t.Hash == hash {
				return t
			}
		}
	}
	return torrent
}

func (ts *TorrentStorage) GetAll(category string, filter string, hashes []string) []*Torrent {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	torrents := make([]*Torrent, 0)
	for _, torrent := range ts.torrents {
		if category != "" && torrent.Category != category {
			continue
		}
		if filter != "" && torrent.State != filter {
			continue
		}
		torrents = append(torrents, torrent)
	}

	if len(hashes) > 0 {
		filtered := make([]*Torrent, 0)
		for _, hash := range hashes {
			for _, torrent := range torrents {
				if torrent.Hash == hash {
					filtered = append(filtered, torrent)
				}
			}
		}
		torrents = filtered
	}
	return torrents
}

func (ts *TorrentStorage) GetAllSorted(category string, filter string, hashes []string, sortBy string, ascending bool) []*Torrent {
	torrents := ts.GetAll(category, filter, hashes)
	if sortBy != "" {
		sort.Slice(torrents, func(i, j int) bool {
			// If ascending is false, swap i and j to get descending order
			if !ascending {
				i, j = j, i
			}

			switch sortBy {
			case "name":
				return torrents[i].Name < torrents[j].Name
			case "size":
				return torrents[i].Size < torrents[j].Size
			case "added_on":
				return torrents[i].AddedOn < torrents[j].AddedOn
			case "completed":
				return torrents[i].Completed < torrents[j].Completed
			case "progress":
				return torrents[i].Progress < torrents[j].Progress
			case "state":
				return torrents[i].State < torrents[j].State
			case "category":
				return torrents[i].Category < torrents[j].Category
			case "dlspeed":
				return torrents[i].Dlspeed < torrents[j].Dlspeed
			case "upspeed":
				return torrents[i].Upspeed < torrents[j].Upspeed
			case "ratio":
				return torrents[i].Ratio < torrents[j].Ratio
			default:
				// Default sort by added_on
				return torrents[i].AddedOn < torrents[j].AddedOn
			}
		})
	}
	return torrents
}

func (ts *TorrentStorage) Update(torrent *Torrent) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.torrents[keyPair(torrent.Hash, torrent.Category)] = torrent
	go func() {
		err := ts.saveToFile()
		if err != nil {
			fmt.Println(err)
		}
	}()
}

func (ts *TorrentStorage) Delete(hash, category string, removeFromDebrid bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	key := keyPair(hash, category)
	torrent, exists := ts.torrents[key]
	if !exists && category == "" {
		// Remove the torrent without knowing the category
		for k, t := range ts.torrents {
			if t.Hash == hash {
				key = k
				torrent = t
				break
			}
		}
	}

	if torrent == nil {
		return
	}
	if removeFromDebrid && torrent.ID != "" && torrent.Debrid != "" {
		dbClient := GetStore().debrid.GetClient(torrent.Debrid)
		if dbClient != nil {
			_ = dbClient.DeleteTorrent(torrent.ID)
		}
	}

	delete(ts.torrents, key)

	// Delete the torrent folder
	if torrent.ContentPath != "" {
		err := os.RemoveAll(torrent.ContentPath)
		if err != nil {
			return
		}
	}
	go func() {
		err := ts.saveToFile()
		if err != nil {
			fmt.Println(err)
		}
	}()
}

func (ts *TorrentStorage) DeleteMultiple(hashes []string, removeFromDebrid bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	toDelete := make(map[string]string)

	for _, hash := range hashes {
		for key, torrent := range ts.torrents {
			if torrent == nil {
				continue
			}
			if torrent.Hash == hash {
				if removeFromDebrid && torrent.ID != "" && torrent.Debrid != "" {
					toDelete[torrent.ID] = torrent.Debrid
				}
				delete(ts.torrents, key)
				if torrent.ContentPath != "" {
					err := os.RemoveAll(torrent.ContentPath)
					if err != nil {
						return
					}
				}
			}
		}
	}
	go func() {
		err := ts.saveToFile()
		if err != nil {
			fmt.Println(err)
		}
	}()

	clients := GetStore().debrid.GetClients()

	go func() {
		for id, debrid := range toDelete {
			dbClient, ok := clients[debrid]
			if !ok {
				continue
			}
			err := dbClient.DeleteTorrent(id)
			if err != nil {
				fmt.Println(err)
			}
		}
	}()
}

func (ts *TorrentStorage) Save() error {
	return ts.saveToFile()
}

// saveToFile is a helper function to write the current state to the JSON file
func (ts *TorrentStorage) saveToFile() error {
	ts.mu.RLock()
	data, err := json.MarshalIndent(ts.torrents, "", "  ")
	ts.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(ts.filename, data, 0644)
}

func (ts *TorrentStorage) Reset() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.torrents = make(Torrents)
}

type Torrent struct {
	ID            string         `json:"id"`
	Debrid        string         `json:"debrid"`
	TorrentPath   string         `json:"-"`
	DebridTorrent *types.Torrent `json:"-"`

	AddedOn           int64   `json:"added_on,omitempty"`
	AmountLeft        int64   `json:"amount_left"`
	AutoTmm           bool    `json:"auto_tmm"`
	Availability      float64 `json:"availability,omitempty"`
	Category          string  `json:"category,omitempty"`
	Completed         int64   `json:"completed"`
	CompletionOn      int     `json:"completion_on,omitempty"`
	ContentPath       string  `json:"content_path"`
	DlLimit           int     `json:"dl_limit"`
	Dlspeed           int64   `json:"dlspeed"`
	Downloaded        int64   `json:"downloaded"`
	DownloadedSession int64   `json:"downloaded_session"`
	Eta               int     `json:"eta"`
	FlPiecePrio       bool    `json:"f_l_piece_prio,omitempty"`
	ForceStart        bool    `json:"force_start,omitempty"`
	Hash              string  `json:"hash"`
	LastActivity      int64   `json:"last_activity,omitempty"`
	MagnetUri         string  `json:"magnet_uri,omitempty"`
	MaxRatio          int     `json:"max_ratio,omitempty"`
	MaxSeedingTime    int     `json:"max_seeding_time,omitempty"`
	Name              string  `json:"name,omitempty"`
	NumComplete       int     `json:"num_complete,omitempty"`
	NumIncomplete     int     `json:"num_incomplete,omitempty"`
	NumLeechs         int     `json:"num_leechs,omitempty"`
	NumSeeds          int     `json:"num_seeds,omitempty"`
	Priority          int     `json:"priority,omitempty"`
	Progress          float64 `json:"progress"`
	Ratio             int     `json:"ratio,omitempty"`
	RatioLimit        int     `json:"ratio_limit,omitempty"`
	SavePath          string  `json:"save_path"`
	SeedingTimeLimit  int     `json:"seeding_time_limit,omitempty"`
	SeenComplete      int64   `json:"seen_complete,omitempty"`
	SeqDl             bool    `json:"seq_dl"`
	Size              int64   `json:"size,omitempty"`
	State             string  `json:"state,omitempty"`
	SuperSeeding      bool    `json:"super_seeding"`
	Tags              string  `json:"tags,omitempty"`
	TimeActive        int     `json:"time_active,omitempty"`
	TotalSize         int64   `json:"total_size,omitempty"`
	Tracker           string  `json:"tracker,omitempty"`
	UpLimit           int64   `json:"up_limit,omitempty"`
	Uploaded          int64   `json:"uploaded,omitempty"`
	UploadedSession   int64   `json:"uploaded_session,omitempty"`
	Upspeed           int64   `json:"upspeed,omitempty"`
	Source            string  `json:"source,omitempty"`

	sync.Mutex
}

func (t *Torrent) IsReady() bool {
	return (t.AmountLeft <= 0 || t.Progress == 1) && t.TorrentPath != ""
}

func (t *Torrent) discordContext() string {
	format := `
		**Name:** %s
		**Arr:** %s
		**Hash:** %s
		**MagnetURI:** %s
		**Debrid:** %s
	`
	return fmt.Sprintf(format, t.Name, t.Category, t.Hash, t.MagnetUri, t.Debrid)
}

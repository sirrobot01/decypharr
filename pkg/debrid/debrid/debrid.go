package debrid

import (
	"fmt"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid/alldebrid"
	"github.com/sirrobot01/decypharr/pkg/debrid/debrid_link"
	"github.com/sirrobot01/decypharr/pkg/debrid/realdebrid"
	"github.com/sirrobot01/decypharr/pkg/debrid/torbox"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"strings"
)

func createDebridClient(dc config.Debrid) (types.Client, error) {
	switch dc.Name {
	case "realdebrid":
		return realdebrid.New(dc)
	case "torbox":
		return torbox.New(dc)
	case "debridlink":
		return debrid_link.New(dc)
	case "alldebrid":
		return alldebrid.New(dc)
	default:
		return realdebrid.New(dc)
	}
}

func ProcessTorrent(d *Engine, magnet *utils.Magnet, a *arr.Arr, isSymlink, overrideDownloadUncached bool) (*types.Torrent, error) {

	debridTorrent := &types.Torrent{
		InfoHash: magnet.InfoHash,
		Magnet:   magnet,
		Name:     magnet.Name,
		Arr:      a,
		Size:     magnet.Size,
		Files:    make(map[string]types.File),
	}

	errs := make([]error, 0, len(d.Clients))

	// Override first, arr second, debrid third

	if overrideDownloadUncached {
		debridTorrent.DownloadUncached = true
	} else if a.DownloadUncached != nil {
		// Arr cached is set
		debridTorrent.DownloadUncached = *a.DownloadUncached
	} else {
		debridTorrent.DownloadUncached = false
	}

	for index, db := range d.Clients {
		logger := db.GetLogger()
		logger.Info().Str("Debrid", db.GetName()).Str("Hash", debridTorrent.InfoHash).Msg("Processing torrent")

		if !overrideDownloadUncached && a.DownloadUncached == nil {
			debridTorrent.DownloadUncached = db.GetDownloadUncached()
		}

		//if db.GetCheckCached() {
		//	hash, exists := db.IsAvailable([]string{debridTorrent.InfoHash})[debridTorrent.InfoHash]
		//	if !exists || !hash {
		//		logger.Info().Msgf("Torrent: %s is not cached", debridTorrent.Name)
		//		continue
		//	} else {
		//		logger.Info().Msgf("Torrent: %s is cached(or downloading)", debridTorrent.Name)
		//	}
		//}

		dbt, err := db.SubmitMagnet(debridTorrent)
		if err != nil || dbt == nil || dbt.Id == "" {
			errs = append(errs, err)
			continue
		}
		dbt.Arr = a
		logger.Info().Str("id", dbt.Id).Msgf("Torrent: %s submitted to %s", dbt.Name, db.GetName())
		d.LastUsed = index

		torrent, err := db.CheckStatus(dbt, isSymlink)
		if err != nil && torrent != nil && torrent.Id != "" {
			// Delete the torrent if it was not downloaded
			go func(id string) {
				_ = db.DeleteTorrent(id)
			}(torrent.Id)
		}
		return torrent, err
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("failed to process torrent: no clients available")
	}
	if len(errs) == 1 {
		return nil, fmt.Errorf("failed to process torrent: %w", errs[0])
	} else {
		errStrings := make([]string, 0, len(errs))
		for _, err := range errs {
			errStrings = append(errStrings, err.Error())
		}
		return nil, fmt.Errorf("failed to process torrent: %s", strings.Join(errStrings, ", "))
	}
}

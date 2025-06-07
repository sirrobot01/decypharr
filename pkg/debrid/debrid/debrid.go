package debrid

import (
	"fmt"
	"strings"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid/alldebrid"
	"github.com/sirrobot01/decypharr/pkg/debrid/debrid_link"
	"github.com/sirrobot01/decypharr/pkg/debrid/realdebrid"
	"github.com/sirrobot01/decypharr/pkg/debrid/torbox"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func createDebridClient(dc config.Debrid) types.Client {
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

		// Log the download uncached decision
		logger.Info().Msgf("Download uncached decision for %s: override=%v, arr=%v, debrid=%v, final=%v",
			debridTorrent.Name, overrideDownloadUncached,
			func() interface{} {
				if a.DownloadUncached != nil {
					return *a.DownloadUncached
				} else {
					return "nil"
				}
			}(),
			db.GetDownloadUncached(), debridTorrent.DownloadUncached)

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
		// Ensure DownloadUncached is preserved from the original torrent
		dbt.DownloadUncached = debridTorrent.DownloadUncached
		logger.Info().Str("id", dbt.Id).Msgf("Torrent: %s submitted to %s (uncached: %v)", dbt.Name, db.GetName(), dbt.DownloadUncached)
		d.LastUsed = index

		// Apply concurrency control for uncached downloads before CheckStatus
		// This prevents overwhelming the debrid service with too many uncached submissions
		var slotAcquired bool
		if dbt.DownloadUncached {
			logger.Info().Msgf("Attempting to acquire uncached slot for %s (provider: %s)", dbt.Name, index)
			if !d.AcquireUncachedSlot(index) {
				logger.Info().Msgf("Max concurrent uncached downloads reached for %s, waiting for available slot", index)
				// Block until a slot becomes available
				d.WaitForUncachedSlot(index)
				logger.Info().Msgf("Acquired uncached download slot for %s", index)
			} else {
				logger.Info().Msgf("Immediately acquired uncached slot for %s", index)
			}
			slotAcquired = true
		} else {
			logger.Info().Msgf("Torrent %s is cached, no slot needed", dbt.Name)
		}

		torrent, err := db.CheckStatus(dbt, isSymlink)

		// Release slot if CheckStatus fails
		if err != nil && slotAcquired {
			logger.Info().Msgf("Releasing uncached slot due to CheckStatus error for %s", index)
			d.ReleaseUncachedSlot(index)
			slotAcquired = false
		}

		// Store slot info for ProcessFiles to handle later
		if slotAcquired {
			torrent.UncachedSlotAcquired = true
			torrent.UncachedSlotProvider = index
		}

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

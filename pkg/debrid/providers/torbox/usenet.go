package torbox

// TorBox usenet downloads in the debrid view.
//
// TorBox downloads (and PAR2-repairs) NZBs server-side and keeps the result in
// the account, exactly like a cached torrent. This lists those finished usenet
// items alongside torrents so they appear in the mount and can be symlinked
// and streamed like any torrent (sirrobot01/decypharr#341).
//
// Usenet items live in a separate TorBox id space, so they carry a "u" prefix
// ("u2515089"). Every provider method that talks to TorBox by id checks the
// prefix and routes to /api/usenet/* instead of /api/torrents/*.
//
// Safety rules:
//   - Only finished, download_present items are listed; in-flight jobs belong
//     to whatever submitted them.
//   - A usenet item cannot be re-added from a magnet, so SubmitMagnet refuses a
//     known usenet hash and DeleteTorrent is a no-op for usenet ids. Repair then
//     fails loudly instead of deleting content it cannot recreate.
//   - If the usenet listing fails, the whole refresh fails: a partial list would
//     make every usenet entry look deleted and break existing symlinks.

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

const usenetIDPrefix = "u"

// usenetID reports whether a decypharr id refers to a TorBox usenet item and
// returns the bare numeric TorBox id.
func usenetID(id string) (string, bool) {
	rest, ok := strings.CutPrefix(id, usenetIDPrefix)
	if !ok || rest == "" {
		return "", false
	}
	if _, err := strconv.Atoi(rest); err != nil {
		return "", false
	}
	return rest, true
}

func (tb *Torbox) isKnownUsenetHash(hash string) bool {
	if hash == "" {
		return false
	}
	_, ok := tb.usenetHashes.Load(strings.ToLower(hash))
	return ok
}

func (tb *Torbox) usenetToTorrent(data *torboxInfo) *types.Torrent {
	id := usenetIDPrefix + strconv.Itoa(data.Id)
	t := &types.Torrent{
		Id:               id,
		Name:             data.Name,
		Bytes:            data.Size,
		Progress:         data.Progress * 100,
		Status:           tb.getTorboxStatus(data.DownloadState, data.DownloadFinished),
		Speed:            data.DownloadSpeed,
		Filename:         data.Name,
		OriginalFilename: data.Name,
		Debrid:           tb.config.Name,
		Files:            make(map[string]types.File),
		Added:            data.CreatedAt,
		InfoHash:         strings.ToLower(data.Hash),
	}
	cfg := config.Get()
	for _, f := range data.Files {
		fileName := filepath.Base(f.Name)
		if err := cfg.ValidateFileAllowed(f.AbsolutePath, f.Size); err != nil {
			continue
		}
		file := types.File{
			TorrentId: id,
			Id:        strconv.Itoa(f.Id),
			Name:      fileName,
			Size:      f.Size,
			Path:      f.Name,
		}
		if data.DownloadFinished {
			file.Link = fmt.Sprintf("torbox://%s/%d", id, f.Id)
		}
		t.Files[fileName] = file
	}
	cleanPath := path.Clean(data.Name)
	if len(data.Files) > 0 {
		cleanPath = path.Clean(data.Files[0].Name)
	}
	t.OriginalFilename = strings.Split(cleanPath, "/")[0]
	return t
}

// usenetListable: only content TorBox has finished and still holds.
func usenetListable(d *torboxInfo) bool {
	return d.DownloadFinished && d.DownloadPresent && len(d.Files) > 0 &&
		(d.DownloadState == "completed" || d.DownloadState == "cached")
}

func (tb *Torbox) getUsenetPage(offset int) ([]torboxInfo, error) {
	var res TorrentsListResponse
	resp, err := tb.doGet("/api/usenet/mylist", map[string]string{
		"bypass_cache": "true",
		"offset":       strconv.Itoa(offset),
	}, &res)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("torbox usenet API error: Status: %d", resp.StatusCode)
	}
	if !res.Success || res.Data == nil {
		return nil, fmt.Errorf("torbox usenet API error: %v", res.Error)
	}
	return *res.Data, nil
}

// getAllUsenet lists every finished usenet item. Any error aborts the whole
// listing (see the partial-list rule at the top of this file).
func (tb *Torbox) getAllUsenet() ([]*types.Torrent, error) {
	out := make([]*types.Torrent, 0)
	offset := 0
	for {
		page, err := tb.getUsenetPage(offset)
		if err != nil {
			return nil, fmt.Errorf("get TorBox usenet at offset %d: %w", offset, err)
		}
		if len(page) == 0 {
			break
		}
		for i := range page {
			d := &page[i]
			tb.usenetPresent.Store(strconv.Itoa(d.Id), d.DownloadPresent)
			if !usenetListable(d) {
				continue
			}
			t := tb.usenetToTorrent(d)
			tb.usenetHashes.Store(t.InfoHash, struct{}{})
			out = append(out, t)
		}
		offset += len(page)
	}
	tb.logger.Debug().Int("count", len(out)).Msg("listed TorBox usenet items")
	return out, nil
}

func (tb *Torbox) getUsenetInfo(uid string) (*torboxInfo, error) {
	var res InfoResponse
	resp, err := tb.doGet("/api/usenet/mylist", map[string]string{"id": uid, "bypass_cache": "true"}, &res)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("torbox usenet API error: Status: %d", resp.StatusCode)
	}
	if res.Data == nil {
		return nil, fmt.Errorf("usenet item %s not found", uid)
	}
	return res.Data, nil
}

func (tb *Torbox) getUsenetTorrent(uid string) (*types.Torrent, error) {
	d, err := tb.getUsenetInfo(uid)
	if err != nil {
		return nil, err
	}
	tb.usenetPresent.Store(uid, d.DownloadPresent)
	t := tb.usenetToTorrent(d)
	tb.usenetHashes.Store(t.InfoHash, struct{}{})
	return t, nil
}

func (tb *Torbox) fetchUsenetDownloadLink(acc *account.Account, uid string, file *types.File) (types.DownloadLink, error) {
	query := url.Values{}
	query.Set("token", acc.Token)
	query.Set("usenet_id", uid)
	query.Set("file_id", file.Id)
	query.Set("redirect", "true")
	now := time.Now()
	return types.DownloadLink{
		Filename:     file.Name,
		Size:         file.Size,
		Token:        tb.APIKey,
		Link:         file.Link,
		DownloadLink: fmt.Sprintf("%s/api/usenet/requestdl?%s", tb.Host, query.Encode()),
		Debrid:       tb.config.Name,
		Id:           file.Id,
		Generated:    now,
		ExpiresAt:    now.Add(tb.autoExpiresLinksAfter),
	}, nil
}

// usenetCheckFile mirrors CheckFile's contract for usenet ids: nil when TorBox
// still holds the content, HosterUnavailable otherwise.
func (tb *Torbox) usenetPresentFor(uid string) (bool, error) {
	if v, ok := tb.usenetPresent.Load(uid); ok {
		return v.(bool), nil
	}
	d, err := tb.getUsenetInfo(uid)
	if err != nil {
		return false, err
	}
	tb.usenetPresent.Store(uid, d.DownloadPresent)
	return d.DownloadPresent, nil
}

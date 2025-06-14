package qbit

import (
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"net/http"
	"path/filepath"
	"strings"
)

func (q *QBit) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_arr := getArr(ctx)
	if _arr == nil {
		// No arr
		_, _ = w.Write([]byte("Ok."))
		return
	}
	if err := _arr.Validate(); err != nil {
		q.logger.Error().Err(err).Msgf("Error validating arr")
	}
	_, _ = w.Write([]byte("Ok."))
}

func (q *QBit) handleVersion(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("v4.3.2"))
}

func (q *QBit) handleWebAPIVersion(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("2.7"))
}

func (q *QBit) handlePreferences(w http.ResponseWriter, r *http.Request) {
	preferences := getAppPreferences()

	preferences.WebUiUsername = q.Username
	preferences.SavePath = q.DownloadFolder
	preferences.TempPath = filepath.Join(q.DownloadFolder, "temp")

	request.JSONResponse(w, preferences, http.StatusOK)
}

func (q *QBit) handleBuildInfo(w http.ResponseWriter, r *http.Request) {
	res := BuildInfo{
		Bitness:    64,
		Boost:      "1.75.0",
		Libtorrent: "1.2.11.0",
		Openssl:    "1.1.1i",
		Qt:         "5.15.2",
		Zlib:       "1.2.11",
	}
	request.JSONResponse(w, res, http.StatusOK)
}

func (q *QBit) handleShutdown(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (q *QBit) handleTorrentsInfo(w http.ResponseWriter, r *http.Request) {
	//log all url params
	ctx := r.Context()
	category := getCategory(ctx)
	filter := strings.Trim(r.URL.Query().Get("filter"), "")
	hashes := getHashes(ctx)
	torrents := q.storage.GetAllSorted(category, filter, hashes, "added_on", false)
	request.JSONResponse(w, torrents, http.StatusOK)
}

func (q *QBit) handleTorrentsAdd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse form based on content type
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			q.logger.Error().Err(err).Msgf("Error parsing multipart form")
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else if strings.Contains(contentType, "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err != nil {
			q.logger.Error().Err(err).Msgf("Error parsing form")
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		http.Error(w, "Invalid content type", http.StatusBadRequest)
		return
	}

	action := "symlink"
	if strings.ToLower(r.FormValue("sequentialDownload")) == "true" {
		action = "download"
	}
	debridName := r.FormValue("debrid")
	category := r.FormValue("category")
	_arr := getArr(ctx)
	if _arr == nil {
		_arr = arr.New(category, "", "", false, false, nil, "")
	}
	atleastOne := false

	// Handle magnet URLs
	if urls := r.FormValue("urls"); urls != "" {
		var urlList []string
		for _, u := range strings.Split(urls, "\n") {
			urlList = append(urlList, strings.TrimSpace(u))
		}
		for _, url := range urlList {
			if err := q.addMagnet(ctx, url, _arr, debridName, action); err != nil {
				q.logger.Error().Err(err).Msgf("Error adding magnet")
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			atleastOne = true
		}
	}

	// Handle torrent files
	if r.MultipartForm != nil && r.MultipartForm.File != nil {
		if files := r.MultipartForm.File["torrents"]; len(files) > 0 {
			for _, fileHeader := range files {
				if err := q.addTorrent(ctx, fileHeader, _arr, debridName, action); err != nil {
					q.logger.Error().Err(err).Msgf("Error adding torrent")
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				atleastOne = true
			}
		}
	}

	if !atleastOne {
		http.Error(w, "No valid URLs or torrents provided", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (q *QBit) handleTorrentsDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hashes := getHashes(ctx)
	if len(hashes) == 0 {
		http.Error(w, "No hashes provided", http.StatusBadRequest)
		return
	}
	category := getCategory(ctx)
	for _, hash := range hashes {
		q.storage.Delete(hash, category, false)
	}

	w.WriteHeader(http.StatusOK)
}

func (q *QBit) handleTorrentsPause(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hashes := getHashes(ctx)
	category := getCategory(ctx)
	for _, hash := range hashes {
		torrent := q.storage.Get(hash, category)
		if torrent == nil {
			continue
		}
		go q.PauseTorrent(torrent)
	}

	w.WriteHeader(http.StatusOK)
}

func (q *QBit) handleTorrentsResume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hashes := getHashes(ctx)
	category := getCategory(ctx)
	for _, hash := range hashes {
		torrent := q.storage.Get(hash, category)
		if torrent == nil {
			continue
		}
		go q.ResumeTorrent(torrent)
	}

	w.WriteHeader(http.StatusOK)
}

func (q *QBit) handleTorrentRecheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hashes := getHashes(ctx)
	category := getCategory(ctx)
	for _, hash := range hashes {
		torrent := q.storage.Get(hash, category)
		if torrent == nil {
			continue
		}
		go q.RefreshTorrent(torrent)
	}

	w.WriteHeader(http.StatusOK)
}

func (q *QBit) handleCategories(w http.ResponseWriter, r *http.Request) {
	var categories = map[string]TorrentCategory{}
	for _, cat := range q.Categories {
		path := filepath.Join(q.DownloadFolder, cat)
		categories[cat] = TorrentCategory{
			Name:     cat,
			SavePath: path,
		}
	}
	request.JSONResponse(w, categories, http.StatusOK)
}

func (q *QBit) handleCreateCategory(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Failed to parse form data", http.StatusBadRequest)
		return
	}

	name := r.Form.Get("category")
	if name == "" {
		http.Error(w, "No name provided", http.StatusBadRequest)
		return
	}

	q.Categories = append(q.Categories, name)

	request.JSONResponse(w, nil, http.StatusOK)
}

func (q *QBit) handleTorrentProperties(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hash := r.URL.Query().Get("hash")
	torrent := q.storage.Get(hash, getCategory(ctx))

	properties := q.GetTorrentProperties(torrent)
	request.JSONResponse(w, properties, http.StatusOK)
}

func (q *QBit) handleTorrentFiles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hash := r.URL.Query().Get("hash")
	torrent := q.storage.Get(hash, getCategory(ctx))
	if torrent == nil {
		return
	}
	request.JSONResponse(w, torrent.Files, http.StatusOK)
}

func (q *QBit) handleSetCategory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	category := getCategory(ctx)
	hashes := getHashes(ctx)
	torrents := q.storage.GetAll("", "", hashes)
	for _, torrent := range torrents {
		torrent.Category = category
		q.storage.AddOrUpdate(torrent)
	}
	request.JSONResponse(w, nil, http.StatusOK)
}

func (q *QBit) handleAddTorrentTags(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Failed to parse form data", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	hashes := getHashes(ctx)
	tags := strings.Split(r.FormValue("tags"), ",")
	for i, tag := range tags {
		tags[i] = strings.TrimSpace(tag)
	}
	torrents := q.storage.GetAll("", "", hashes)
	for _, t := range torrents {
		q.setTorrentTags(t, tags)
	}
	request.JSONResponse(w, nil, http.StatusOK)
}

func (q *QBit) handleRemoveTorrentTags(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Failed to parse form data", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	hashes := getHashes(ctx)
	tags := strings.Split(r.FormValue("tags"), ",")
	for i, tag := range tags {
		tags[i] = strings.TrimSpace(tag)
	}
	torrents := q.storage.GetAll("", "", hashes)
	for _, torrent := range torrents {
		q.removeTorrentTags(torrent, tags)

	}
	request.JSONResponse(w, nil, http.StatusOK)
}

func (q *QBit) handleGetTags(w http.ResponseWriter, r *http.Request) {
	request.JSONResponse(w, q.Tags, http.StatusOK)
}

func (q *QBit) handleCreateTags(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Failed to parse form data", http.StatusBadRequest)
		return
	}
	tags := strings.Split(r.FormValue("tags"), ",")
	for i, tag := range tags {
		tags[i] = strings.TrimSpace(tag)
	}
	q.addTags(tags)
	request.JSONResponse(w, nil, http.StatusOK)
}

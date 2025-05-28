package qbit

import (
	"context"
	"encoding/base64"
	"github.com/go-chi/chi/v5"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/service"
	"net/http"
	"path/filepath"
	"strings"
)

func decodeAuthHeader(header string) (string, string, error) {
	encodedTokens := strings.Split(header, " ")
	if len(encodedTokens) != 2 {
		return "", "", nil
	}
	encodedToken := encodedTokens[1]

	bytes, err := base64.StdEncoding.DecodeString(encodedToken)
	if err != nil {
		return "", "", err
	}

	bearer := string(bytes)

	colonIndex := strings.LastIndex(bearer, ":")
	host := bearer[:colonIndex]
	token := bearer[colonIndex+1:]

	return host, token, nil
}

func (q *QBit) CategoryContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		category := strings.Trim(r.URL.Query().Get("category"), "")
		if category == "" {
			// Get from form
			_ = r.ParseForm()
			category = r.Form.Get("category")
			if category == "" {
				// Get from multipart form
				_ = r.ParseMultipartForm(32 << 20)
				category = r.FormValue("category")
			}
		}
		ctx := context.WithValue(r.Context(), "category", strings.TrimSpace(category))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (q *QBit) authContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, token, err := decodeAuthHeader(r.Header.Get("Authorization"))
		category := r.Context().Value("category").(string)
		svc := service.GetService()
		// Check if arr exists
		a := svc.Arr.Get(category)
		if a == nil {
			downloadUncached := false
			a = arr.New(category, "", "", false, false, &downloadUncached)
		}
		if err == nil {
			host = strings.TrimSpace(host)
			if host != "" {
				a.Host = host
			}
			token = strings.TrimSpace(token)
			if token != "" {
				a.Token = token
			}
		}

		svc.Arr.AddOrUpdate(a)
		ctx := context.WithValue(r.Context(), "arr", a)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func HashesCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_hashes := chi.URLParam(r, "hashes")
		var hashes []string
		if _hashes != "" {
			hashes = strings.Split(_hashes, "|")
		}
		if hashes == nil {
			// Get hashes from form
			_ = r.ParseForm()
			hashes = r.Form["hashes"]
		}
		for i, hash := range hashes {
			hashes[i] = strings.TrimSpace(hash)
		}
		ctx := context.WithValue(r.Context(), "hashes", hashes)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (q *QBit) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_arr := ctx.Value("arr").(*arr.Arr)
	if _arr == nil {
		// No arr
		_, _ = w.Write([]byte("Ok."))
		return
	}
	if err := _arr.Validate(); err != nil {
		q.logger.Info().Msgf("Error validating arr: %v", err)
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
	preferences := NewAppPreferences()

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
	category := ctx.Value("category").(string)
	filter := strings.Trim(r.URL.Query().Get("filter"), "")
	hashes, _ := ctx.Value("hashes").([]string)
	torrents := q.Storage.GetAllSorted(category, filter, hashes, "added_on", false)
	request.JSONResponse(w, torrents, http.StatusOK)
}

func (q *QBit) handleTorrentsAdd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse form based on content type
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			q.logger.Info().Msgf("Error parsing multipart form: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else if strings.Contains(contentType, "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err != nil {
			q.logger.Info().Msgf("Error parsing form: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		http.Error(w, "Invalid content type", http.StatusBadRequest)
		return
	}

	isSymlink := strings.ToLower(r.FormValue("sequentialDownload")) != "true"
	category := r.FormValue("category")
	atleastOne := false
	ctx = context.WithValue(ctx, "isSymlink", isSymlink)

	// Handle magnet URLs
	if urls := r.FormValue("urls"); urls != "" {
		var urlList []string
		for _, u := range strings.Split(urls, "\n") {
			urlList = append(urlList, strings.TrimSpace(u))
		}
		for _, url := range urlList {
			if err := q.AddMagnet(ctx, url, category); err != nil {
				q.logger.Info().Msgf("Error adding magnet: %v", err)
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
				if err := q.AddTorrent(ctx, fileHeader, category); err != nil {
					q.logger.Info().Msgf("Error adding torrent: %v", err)
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
	hashes, _ := ctx.Value("hashes").([]string)
	if len(hashes) == 0 {
		http.Error(w, "No hashes provided", http.StatusBadRequest)
		return
	}
	category := ctx.Value("category").(string)
	for _, hash := range hashes {
		q.Storage.Delete(hash, category, false)
	}

	w.WriteHeader(http.StatusOK)
}

func (q *QBit) handleTorrentsPause(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hashes, _ := ctx.Value("hashes").([]string)
	category := ctx.Value("category").(string)
	for _, hash := range hashes {
		torrent := q.Storage.Get(hash, category)
		if torrent == nil {
			continue
		}
		go q.PauseTorrent(torrent)
	}

	w.WriteHeader(http.StatusOK)
}

func (q *QBit) handleTorrentsResume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hashes, _ := ctx.Value("hashes").([]string)
	category := ctx.Value("category").(string)
	for _, hash := range hashes {
		torrent := q.Storage.Get(hash, category)
		if torrent == nil {
			continue
		}
		go q.ResumeTorrent(torrent)
	}

	w.WriteHeader(http.StatusOK)
}

func (q *QBit) handleTorrentRecheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hashes, _ := ctx.Value("hashes").([]string)
	category := ctx.Value("category").(string)
	for _, hash := range hashes {
		torrent := q.Storage.Get(hash, category)
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
	torrent := q.Storage.Get(hash, ctx.Value("category").(string))

	properties := q.GetTorrentProperties(torrent)
	request.JSONResponse(w, properties, http.StatusOK)
}

func (q *QBit) handleTorrentFiles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hash := r.URL.Query().Get("hash")
	torrent := q.Storage.Get(hash, ctx.Value("category").(string))
	if torrent == nil {
		return
	}
	files := q.GetTorrentFiles(torrent)
	request.JSONResponse(w, files, http.StatusOK)
}

func (q *QBit) handleSetCategory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	category := ctx.Value("category").(string)
	hashes, _ := ctx.Value("hashes").([]string)
	torrents := q.Storage.GetAll("", "", hashes)
	for _, torrent := range torrents {
		torrent.Category = category
		q.Storage.AddOrUpdate(torrent)
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
	hashes, _ := ctx.Value("hashes").([]string)
	tags := strings.Split(r.FormValue("tags"), ",")
	for i, tag := range tags {
		tags[i] = strings.TrimSpace(tag)
	}
	torrents := q.Storage.GetAll("", "", hashes)
	for _, t := range torrents {
		q.SetTorrentTags(t, tags)
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
	hashes, _ := ctx.Value("hashes").([]string)
	tags := strings.Split(r.FormValue("tags"), ",")
	for i, tag := range tags {
		tags[i] = strings.TrimSpace(tag)
	}
	torrents := q.Storage.GetAll("", "", hashes)
	for _, torrent := range torrents {
		q.RemoveTorrentTags(torrent, tags)

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
	q.AddTags(tags)
	request.JSONResponse(w, nil, http.StatusOK)
}

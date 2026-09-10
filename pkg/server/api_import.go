package server

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sourcegraph/conc/iter"
)

func (s *Server) handleAddContent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	arrName := r.FormValue("arr")
	action := r.FormValue("action")
	debridName := r.FormValue("debrid")
	callbackUrl := r.FormValue("callbackUrl")
	downloadFolder := r.FormValue("downloadFolder")
	if downloadFolder == "" {
		downloadFolder = config.Get().DownloadFolder
	}
	skipMultiSeason := r.FormValue("skipMultiSeason") == "true"

	dlUncached := r.FormValue("downloadUncached") == "true"
	var downloadUncached *bool
	if dlUncached {
		downloadUncached = &dlUncached
	}
	rmTrackerUrls := r.FormValue("rmTrackerUrls") == "true"

	// Check config setting - if always remove tracker URLs is enabled, force it to true
	cfg := config.Get()
	if cfg.AlwaysRmTrackerUrls {
		rmTrackerUrls = true
	}

	// A category with no configured Arr is a throwaway that only routes the
	// download.
	instance, known := s.manager.Arr().Get(arrName)
	if !known {
		instance = arr.Arr{Name: arrName}
	}

	type addTask struct {
		request *manager.ImportRequest
		source  string
	}
	var tasks []addTask
	results := make([]*manager.ImportRequest, 0)
	defer r.MultipartForm.RemoveAll()

	// Collect torrent URLs
	if urls := r.FormValue("urls"); urls != "" {
		for u := range strings.SplitSeq(urls, "\n") {
			if trimmed := strings.TrimSpace(u); trimmed != "" {
				magnet, err := utils.GetMagnetFromUrl(trimmed, rmTrackerUrls)
				if err != nil {
					results = append(results, &manager.ImportRequest{Status: "error", Error: fmt.Sprintf("Failed to parse URL %s: %v", trimmed, err)})
					continue
				}
				req := manager.NewTorrentRequest(debridName, downloadFolder, magnet, instance, config.DownloadAction(action), downloadUncached, callbackUrl, manager.ImportTypeAPI, skipMultiSeason)
				results = append(results, req)
				tasks = append(tasks, addTask{request: req, source: trimmed})
			}
		}
	}

	// Collect torrent files
	if files := r.MultipartForm.File["files"]; len(files) > 0 {
		for _, fileHeader := range files {
			file, err := fileHeader.Open()
			if err != nil {
				results = append(results, &manager.ImportRequest{Status: "error", Error: fmt.Sprintf("Failed to open file %s: %v", fileHeader.Filename, err)})
				continue
			}

			magnet, err := utils.GetMagnetFromFile(file, fileHeader.Filename, rmTrackerUrls)
			_ = file.Close()
			if err != nil {
				results = append(results, &manager.ImportRequest{Status: "error", Error: fmt.Sprintf("Failed to parse torrent file %s: %v", fileHeader.Filename, err)})
				continue
			}
			req := manager.NewTorrentRequest(debridName, downloadFolder, magnet, instance, config.DownloadAction(action), downloadUncached, callbackUrl, manager.ImportTypeAPI, skipMultiSeason)
			results = append(results, req)
			tasks = append(tasks, addTask{request: req, source: fileHeader.Filename})
		}
	}

	// Collect NZB URLs
	if nzbURLs := r.FormValue("nzbURLs"); nzbURLs != "" {
		for u := range strings.SplitSeq(nzbURLs, "\n") {
			if trimmed := strings.TrimSpace(u); trimmed != "" {
				filename, content, err := utils.DownloadFile(trimmed, utils.WithHeader("User-Agent", s.nzbUserAgent))
				if err != nil {
					results = append(results, &manager.ImportRequest{Status: "error", Error: fmt.Sprintf("Failed to fetch NZB from URL %s: %v", trimmed, err)})
					continue
				}
				req := manager.NewNZBRequest(filename, downloadFolder, content, instance, config.DownloadAction(action), callbackUrl, manager.ImportTypeAPI, skipMultiSeason)
				results = append(results, req)
				tasks = append(tasks, addTask{request: req, source: trimmed})
			}
		}
	}

	// Collect NZB files
	if nzbFiles := r.MultipartForm.File["nzbFiles"]; len(nzbFiles) > 0 {
		for _, fileHeader := range nzbFiles {
			content, err := getNZBContentFromFile(fileHeader)
			if err != nil {
				results = append(results, &manager.ImportRequest{Status: "error", Error: fmt.Sprintf("Failed to read NZB file %s: %v", fileHeader.Filename, err)})
				continue
			}
			req := manager.NewNZBRequest(fileHeader.Filename, downloadFolder, content, instance, config.DownloadAction(action), callbackUrl, manager.ImportTypeAPI, skipMultiSeason)
			results = append(results, req)
			tasks = append(tasks, addTask{request: req, source: fileHeader.Filename})
		}
	}

	// Only prepared inputs enter the bounded submission phase.
	submitter := iter.Iterator[addTask]{MaxGoroutines: 10}
	submitter.ForEach(tasks, func(task *addTask) {
		req := task.request
		var err error
		if req.Magnet != nil {
			err = s.manager.AddNewTorrent(ctx, req)
		} else {
			req.Id, err = s.manager.AddNewNZB(ctx, req)
		}
		if err != nil {
			s.logger.Error().Err(err).Str("source", task.source).Msg("Failed to import content")
			req.Error, req.Status = err.Error(), "error"
		}
	})
	utils.JSONResponse(w, results, http.StatusOK)
}

func getNZBContentFromFile(fileHeader *multipart.FileHeader) ([]byte, error) {
	file, err := fileHeader.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Read NZB content
	nzbContent, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	return nzbContent, nil
}

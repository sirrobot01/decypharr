package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	json "github.com/bytedance/sonic"

	"github.com/go-chi/chi/v5"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr/reacquire"
)

// arrBindingSearchMax bounds a binding search: the index can hold hundreds of
// thousands of bindings and the picker only shows a page of them.
const arrBindingSearchMax = 50

func (s *Server) handleListArrReacquireJobs(w http.ResponseWriter, _ *http.Request) {
	service := s.manager.ArrService()
	if service == nil {
		s.sendJSONError(w, "Arr reacquisition service is not available", http.StatusServiceUnavailable)
		return
	}
	utils.JSONResponse(w, service.Jobs(), http.StatusOK)
}

func (s *Server) handleGetArrReacquireJob(w http.ResponseWriter, r *http.Request) {
	service := s.manager.ArrService()
	if service == nil {
		s.sendJSONError(w, "Arr reacquisition service is not available", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		s.sendJSONError(w, "Reacquisition job ID is required", http.StatusBadRequest)
		return
	}
	job, ok := service.Job(id)
	if !ok {
		s.sendJSONError(w, "Reacquisition job not found", http.StatusNotFound)
		return
	}
	utils.JSONResponse(w, job, http.StatusOK)
}

func (s *Server) handleDeleteArrReacquireJobs(w http.ResponseWriter, r *http.Request) {
	service := s.manager.ArrService()
	if service == nil {
		s.sendJSONError(w, "Arr reacquisition service is not available", http.StatusServiceUnavailable)
		return
	}

	var request struct {
		IDs []string `json:"ids"`
	}
	if err := json.ConfigDefault.NewDecoder(r.Body).Decode(&request); err != nil {
		s.sendJSONError(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	ids := make([]string, 0, len(request.IDs))
	seen := make(map[string]struct{}, len(request.IDs))
	for _, id := range request.IDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		s.sendJSONError(w, "At least one reacquisition job ID is required", http.StatusBadRequest)
		return
	}

	deleted, err := service.DeleteJobs(ids)
	if err != nil {
		switch {
		case errors.Is(err, reacquire.ErrJobNotTerminal):
			s.sendJSONError(w, err.Error(), http.StatusConflict)
		case errors.Is(err, reacquire.ErrServiceNotStarted), errors.Is(err, reacquire.ErrServiceClosed):
			s.sendJSONError(w, err.Error(), http.StatusServiceUnavailable)
		default:
			s.logger.Error().Err(err).Msg("Failed to delete Arr reacquisition jobs")
			s.sendJSONError(w, "Failed to delete Arr reacquisition jobs", http.StatusInternalServerError)
		}
		return
	}

	utils.JSONResponse(w, map[string]int{"deleted": deleted}, http.StatusOK)
}

func (s *Server) handleArrReacquire(w http.ResponseWriter, r *http.Request) {
	service := s.manager.ArrService()
	if service == nil {
		s.sendJSONError(w, "Arr reacquisition service is not available", http.StatusServiceUnavailable)
		return
	}

	var request reacquire.Request
	if err := json.ConfigDefault.NewDecoder(r.Body).Decode(&request); err != nil {
		s.sendJSONError(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if request.Cause == "" {
		request.Cause = reacquire.CauseManual
	}
	request.EntryID = strings.TrimSpace(request.EntryID)
	request.FileID = strings.TrimSpace(request.FileID)
	if message := validateArrReacquireRequest(request); message != "" {
		s.sendJSONError(w, message, http.StatusBadRequest)
		return
	}

	job, err := service.Reacquire(request)
	if err != nil {
		s.handleArrReacquireError(w, err)
		return
	}
	utils.JSONResponse(w, job, http.StatusAccepted)
}

func (s *Server) handleRefreshArrIndex(w http.ResponseWriter, _ *http.Request) {
	if s.manager.ArrService() == nil {
		s.sendJSONError(w, "Arr reacquisition service is not available", http.StatusServiceUnavailable)
		return
	}
	if !s.manager.RefreshArrIndex() {
		s.sendJSONError(w, "Arr index refresh could not be queued", http.StatusServiceUnavailable)
		return
	}
	utils.JSONResponse(w, map[string]string{"status": "queued"}, http.StatusAccepted)
}

func (s *Server) handleArrReacquireError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, reacquire.ErrBindingNotFound):
		s.sendJSONError(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, reacquire.ErrBindingUnsafe):
		s.sendJSONError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, reacquire.ErrServiceNotStarted), errors.Is(err, reacquire.ErrServiceClosed):
		s.sendJSONError(w, err.Error(), http.StatusServiceUnavailable)
	default:
		s.logger.Error().Err(err).Msg("Failed to queue Arr reacquisition")
		s.sendJSONError(w, "Failed to queue Arr reacquisition", http.StatusInternalServerError)
	}
}

func validateArrReacquireRequest(request reacquire.Request) string {
	switch {
	case request.EntryID == "":
		return "entryId is required"
	case request.FileID == "":
		return "fileId is required"
	case request.Cause != reacquire.CauseStream && request.Cause != reacquire.CauseRepair && request.Cause != reacquire.CauseManual:
		return "cause must be stream, repair, or manual"
	case request.Strategy != "" && request.Strategy != reacquire.StrategyHistoryFailed && request.Strategy != reacquire.StrategyInteractiveBest && request.Strategy != reacquire.StrategyCommandSearch:
		return "strategy must be history_failed, interactive_best, or command_search"
	default:
		return ""
	}
}

func (s *Server) handleGetArrIndex(w http.ResponseWriter, _ *http.Request) {
	service := s.manager.ArrService()
	if service == nil {
		s.sendJSONError(w, "Arr reacquisition service is not available", http.StatusServiceUnavailable)
		return
	}
	utils.JSONResponse(w, service.IndexSummary(), http.StatusOK)
}

func (s *Server) handleSearchArrBindings(w http.ResponseWriter, r *http.Request) {
	service := s.manager.ArrService()
	if service == nil {
		s.sendJSONError(w, "Arr reacquisition service is not available", http.StatusServiceUnavailable)
		return
	}
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 || limit > arrBindingSearchMax {
		limit = arrBindingSearchMax
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	arrName := strings.TrimSpace(r.URL.Query().Get("arr"))
	utils.JSONResponse(w, service.SearchBindings(arrName, query, limit), http.StatusOK)
}

package server

import (
	"cmp"
	"net/http"
	"strings"

	json "github.com/bytedance/sonic"
)

// handleTautulli handles webhooks from Tautulli. When the payload includes a
// tvdb/tmdb id (or a generic media_id), the repair system runs a targeted
// recheck against that specific media — the v2 equivalent of v1's
// "media-id-scoped repair job". When no media id is supplied the webhook
// falls back to a full manual sweep.
func (s *Server) handleTautulli(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		Topic   string `json:"topic"`
		Arr     string `json:"arr,omitempty"`
		MediaID string `json:"media_id,omitempty"`
		TvdbID  string `json:"tvdb_id,omitempty"`
		TmdbID  string `json:"tmdb_id,omitempty"`
		Fix     bool   `json:"fix,omitempty"`
	}
	if err := json.ConfigDefault.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.logger.Error().Err(err).Msg("Failed to parse webhook body")
		http.Error(w, "Failed to parse webhook body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if payload.Topic != "tautulli" {
		http.Error(w, "Invalid topic", http.StatusBadRequest)
		return
	}

	svc := s.manager.Repair()
	if svc == nil {
		http.Error(w, "Repair service not available", http.StatusServiceUnavailable)
		return
	}

	mediaID := strings.TrimSpace(cmp.Or(payload.MediaID, payload.TmdbID, payload.TvdbID))
	if mediaID == "" {
		// No targeting → fall back to a full sweep.
		if _, err := svc.RunNow(false); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	run, err := svc.RecheckMedia(s.manager.Context(), strings.TrimSpace(payload.Arr), mediaID, payload.Fix)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already running") {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}

	if run != nil {
		s.logger.Info().
			Str("run_id", run.ID).
			Str("arr", payload.Arr).
			Str("media_id", mediaID).
			Bool("fix", payload.Fix).
			Msg("Tautulli webhook: media recheck triggered")
	}
	w.WriteHeader(http.StatusOK)
}

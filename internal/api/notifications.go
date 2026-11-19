package api

import (
	"net/http"
	"strconv"

	"wera/internal/store"
)

// listNotifications is GET /api/notifications: the newest notices and how
// many are unread.
func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, unread, err := store.ListNotifications(r.Context(), s.Pool, currentUser(r).ID, limit)
	if err != nil {
		s.Log.Error("list notifications failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"notifications": items, "unread": unread})
}

// readNotifications is POST /api/notifications/read: {"ids": [...]} marks
// those read; no ids marks them all.
func (s *Server) readNotifications(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if len(body.IDs) > 500 {
		s.writeError(w, http.StatusBadRequest, "too many ids")
		return
	}
	if err := store.MarkNotificationsRead(r.Context(), s.Pool, currentUser(r).ID, body.IDs); err != nil {
		s.Log.Error("mark notifications failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

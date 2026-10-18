package api

import (
	"net/http"
	"strconv"

	"github.com/agribid/esurvey-platform/backend/internal/notify"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// The console's bell.
//
// Every route here is scoped to the calling officer by their own principal,
// never by an id in the path or the body. There is deliberately no way to ask
// for somebody else's notifications: the resource is "mine", so the only
// authorization question is whether the caller is signed in.

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	actor := rbac.FromContext(r.Context())

	limit := 30
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}

	items, err := s.Notifications.List(r.Context(), notify.ListFilter{
		UserID:     actor.UserID,
		UnreadOnly: r.URL.Query().Get("unread") == "true",
		Limit:      limit,
	})
	if err != nil {
		s.Log.Error("listing notifications",
			"request_id", RequestID(r.Context()), "error", err)
		writeError(w, http.StatusInternalServerError, "NOTIFICATIONS_FAILED", "")
		return
	}

	unread, err := s.Notifications.Unread(r.Context(), actor.UserID)
	if err != nil {
		s.Log.Error("counting unread notifications",
			"request_id", RequestID(r.Context()), "error", err)
		writeError(w, http.StatusInternalServerError, "NOTIFICATIONS_FAILED", "")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"unread": unread,
	})
}

func (s *Server) markNotificationRead(w http.ResponseWriter, r *http.Request) {
	actor := rbac.FromContext(r.Context())

	if err := s.Notifications.MarkRead(r.Context(), actor.UserID, r.PathValue("id")); err != nil {
		s.Log.Error("marking a notification read",
			"request_id", RequestID(r.Context()), "error", err)
		writeError(w, http.StatusInternalServerError, "NOTIFICATIONS_FAILED", "")
		return
	}

	unread, err := s.Notifications.Unread(r.Context(), actor.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "NOTIFICATIONS_FAILED", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unread": unread})
}

func (s *Server) markAllNotificationsRead(w http.ResponseWriter, r *http.Request) {
	actor := rbac.FromContext(r.Context())

	n, err := s.Notifications.MarkAllRead(r.Context(), actor.UserID)
	if err != nil {
		s.Log.Error("clearing notifications",
			"request_id", RequestID(r.Context()), "error", err)
		writeError(w, http.StatusInternalServerError, "NOTIFICATIONS_FAILED", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": n, "unread": 0})
}

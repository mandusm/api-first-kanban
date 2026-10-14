package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"api-first-kanban/internal/events"
)

// handleEvents streams SSE events filtered by optional board_id /
// workspace_id query params (none = all). Heartbeat every 15s.
func (a *API) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	filter := events.Filter{
		WorkspaceID: r.URL.Query().Get("workspace_id"),
		BoardID:     r.URL.Query().Get("board_id"),
	}
	// Board-filtered subscribers also receive their workspace's shared
	// events (labels etc.): resolve the board's workspace once.
	if filter.BoardID != "" && filter.WorkspaceID == "" {
		id, ok := pathID(w, filter.BoardID)
		if !ok {
			return
		}
		wsID, err := a.svc.BoardWorkspaceID(r.Context(), id)
		if err != nil {
			writeServiceErr(w, err)
			return
		}
		filter.WorkspaceID = wsID
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch, cancel := a.hub.Subscribe(filter)
	defer cancel()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	// Initial comment so clients immediately see a response.
	fmt.Fprint(w, ": stream open\n\n")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", msg.Payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

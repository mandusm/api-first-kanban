package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"api-first-kanban/internal/events"
	"api-first-kanban/internal/service"
)

// API wires services to HTTP routes.
type API struct {
	svc *service.Service
	hub *events.Hub
}

// Register mounts API routes onto mux.
func Register(mux *http.ServeMux, svc *service.Service, hub *events.Hub) {
	a := &API{svc: svc, hub: hub}
	mux.HandleFunc("GET /api/healthz", a.handleHealthz)
	mux.HandleFunc("GET /api/workspaces", a.handleListWorkspaces)
	mux.HandleFunc("POST /api/workspaces", a.handleCreateWorkspace)
	mux.HandleFunc("GET /api/workspaces/{id}/boards", a.handleListBoards)
	mux.HandleFunc("POST /api/workspaces/{id}/boards", a.handleCreateBoard)
	mux.HandleFunc("GET /api/boards/{id}", a.handleGetBoard)
	mux.HandleFunc("POST /api/boards/{id}/columns", a.handleCreateColumn)
	mux.HandleFunc("PATCH /api/columns/{id}", a.handleUpdateColumn)
	mux.HandleFunc("POST /api/columns/{id}/move", a.handleMoveColumn)
	mux.HandleFunc("DELETE /api/columns/{id}", a.handleDeleteColumn)
	mux.HandleFunc("GET /api/boards/{id}/items", a.handleListBoardItems)
	mux.HandleFunc("POST /api/boards/{id}/items", a.handleCreateItem)
	mux.HandleFunc("GET /api/items/{id}", a.handleGetItem)
	mux.HandleFunc("PATCH /api/items/{id}", a.handleUpdateItem)
	mux.HandleFunc("POST /api/items/{id}/move", a.handleMoveItem)
	mux.HandleFunc("POST /api/items/{id}/transfer", a.handleTransferItem)
	mux.HandleFunc("POST /api/items/{id}/archive", a.handleArchiveItem)
	mux.HandleFunc("POST /api/items/{id}/unarchive", a.handleUnarchiveItem)
	mux.HandleFunc("POST /api/items/{id}/relations", a.handleCreateRelation)
	mux.HandleFunc("DELETE /api/relations/{id}", a.handleDeleteRelation)
	mux.HandleFunc("GET /api/items/{id}/comments", a.handleListComments)
	mux.HandleFunc("POST /api/items/{id}/comments", a.handleAddComment)
	mux.HandleFunc("GET /api/workspaces/{id}/labels", a.handleListLabels)
	mux.HandleFunc("POST /api/workspaces/{id}/labels", a.handleCreateLabel)
	mux.HandleFunc("PATCH /api/labels/{id}", a.handleUpdateLabel)
	mux.HandleFunc("DELETE /api/labels/{id}", a.handleDeleteLabel)
	mux.HandleFunc("GET /api/events", a.handleEvents)
}

// --- error handling ---

type errorBody struct {
	Error errDetail `json:"error"`
}

type errDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: errDetail{Code: code, Message: msg}})
}

// writeServiceErr maps the service sentinel errors onto HTTP statuses.
func writeServiceErr(w http.ResponseWriter, err error) {
	var se *service.ServiceError
	if errors.As(err, &se) {
		switch se.Sentinel() {
		case service.ErrInvalid:
			writeErr(w, http.StatusBadRequest, "validation", se.Message())
			return
		case service.ErrNotFound:
			writeErr(w, http.StatusNotFound, "not_found", se.Message())
			return
		case service.ErrConflict:
			writeErr(w, http.StatusConflict, "conflict", se.Message())
			return
		case service.ErrForbidden:
			writeErr(w, http.StatusForbidden, "forbidden", se.Message())
			return
		}
	}
	slog.Error("internal error", "err", err)
	writeErr(w, http.StatusInternalServerError, "internal", "internal error")
}

// decodeJSON reads a JSON body of the requested shape, limiting size and
// rejecting malformed input. An empty body leaves the zero value.
func decodeJSON[T any](w http.ResponseWriter, r *http.Request, dst *T) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "validation", "cannot read request body")
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return true
	}
	if err := json.Unmarshal(body, dst); err != nil {
		writeErr(w, http.StatusBadRequest, "validation", fmt.Sprintf("invalid JSON: %v", err))
		return false
	}
	return true
}

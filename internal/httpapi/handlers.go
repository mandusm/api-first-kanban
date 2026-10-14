package httpapi

import (
	"context"
	"net/http"
	"os"
	"runtime/debug"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"api-first-kanban/internal/config"
	"api-first-kanban/internal/db"
	"api-first-kanban/internal/search"
	"api-first-kanban/internal/service"
)

type healthzResponse struct {
	Status string `json:"status"`
	DB     bool   `json:"db"`
	Commit string `json:"commit"`
}

func (a *API) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	commit := buildCommit()
	if err := a.svc.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, healthzResponse{Status: "ok", DB: false, Commit: commit})
		return
	}
	writeJSON(w, http.StatusOK, healthzResponse{Status: "ok", DB: true, Commit: commit})
}

// buildCommit identifies the build a response came from, so a live process can
// be checked against git HEAD ("merged but not deployed" drift). The Go
// toolchain stamps vcs.revision/vcs.modified automatically for go build/run in
// a git checkout; a dirty working tree is marked with a "-dirty" suffix.
// KANBAN_COMMIT overrides it for builds without VCS stamping; otherwise the
// value is "unknown".
func buildCommit() string {
	if v := os.Getenv(config.EnvCommit); v != "" {
		return v
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	revision := ""
	dirty := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if revision == "" {
		return "unknown"
	}
	if dirty {
		return revision + "-dirty"
	}
	return revision
}

// --- workspaces ---

type createWorkspaceReq struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Agent       *string `json:"agent"`
}

func (a *API) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	ws, err := a.svc.ListWorkspaces(r.Context())
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workspaceListResp{Workspaces: ws})
}

// handleCreateWorkspace creates a workspaces; 409 on duplicate name.
func (a *API) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var req createWorkspaceReq
	if !decodeJSON(w, r, &req) {
		return
	}
	ws, err := a.svc.CreateWorkspace(r.Context(), req.Name, req.Description, service.NormalizeAgentPtr(req.Agent))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, workspaceResp{Workspace: ws})
}

// Typed response envelopes: single-resource mutation responses are wrapped
// ({workspace}, {board}, {column}, {item}, {label}, {comment}); lists are
// plural ({workspaces}, {boards}, {labels}, {comments}).
type workspaceListResp struct {
	Workspaces []db.Workspace `json:"workspaces"`
}

type workspaceResp struct {
	Workspace db.Workspace `json:"workspace"`
}

type boardListResp struct {
	Boards []db.Board `json:"boards"`
}

// boardCreatedResp: {board:{id,workspace_id,name,key_prefix,columns:[...]}}.
type boardCreatedResp struct {
	Board struct {
		ID          string      `json:"id"`
		WorkspaceID string      `json:"workspace_id"`
		Name        string      `json:"name"`
		KeyPrefix   string      `json:"key_prefix"`
		Columns     []db.Column `json:"columns"`
	} `json:"board"`
}

type columnResp struct {
	Column db.Column `json:"column"`
}

type labelResp struct {
	Label db.Label `json:"label"`
}

type labelsResp struct {
	Labels []db.Label `json:"labels"`
}

// labelsRankedResp is the PB-73 ranked response: same envelope key, each
// label extended with usage_count.
type labelsRankedResp struct {
	Labels []service.LabelUsage `json:"labels"`
}

type commentsResp struct {
	Comments []db.Comment `json:"comments"`
}

type commentResp struct {
	Comment db.Comment `json:"comment"`
}

// --- boards ---

type createBoardReq struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Agent   *string  `json:"agent"`
}

func (a *API) handleListBoards(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	boards, err := a.svc.ListBoards(r.Context(), id)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, boardListResp{Boards: boards})
}

func (a *API) handleCreateBoard(w http.ResponseWriter, r *http.Request) {
	var req createBoardReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	snap, err := a.svc.CreateBoard(r.Context(), id, req.Name, req.Columns, service.NormalizeAgentPtr(req.Agent))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	cols := columnsOf(snap)
	var resp boardCreatedResp
	resp.Board.ID = snap.Board.ID
	resp.Board.WorkspaceID = snap.Board.WorkspaceID
	resp.Board.Name = snap.Board.Name
	resp.Board.KeyPrefix = snap.Board.KeyPrefix
	resp.Board.Columns = cols
	writeJSON(w, http.StatusCreated, resp)
}

// columnsOf extracts the plain column shape (id/board_id/name/position) from a
// snapshot for the create-board response. item_count is snapshot-only, so it is
// not part of this response.
func columnsOf(s service.BoardSnapshot) []db.Column {
	cols := make([]db.Column, 0, len(s.Columns))
	for _, c := range s.Columns {
		cols = append(cols, db.Column{ID: c.ID, BoardID: c.BoardID, Name: c.Name, Position: c.Position})
	}
	return cols
}

func (a *API) handleGetBoard(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	var (
		snap service.BoardSnapshot
		err  error
	)
	// Non-blank q (spec §1.1 ASCII whitespace, search.IsBlank) selects the
	// searched view: one search.Parse per request, then the full composite
	// with FILTERED items. Blank/absent q keeps today's behavior exactly
	// (lazy include_items flow).
	if q := r.URL.Query().Get("q"); !search.IsBlank(q) {
		expr, perr := search.Parse(q)
		if perr != nil {
			// Spec §6: every parse failure is a 400 validation carrying the
			// ParseError message ("parse error at <line>:<col> (offset <n>): …")
			// verbatim; any other error shape also maps to 400 validation
			// (defensive; should not happen).
			writeErr(w, http.StatusBadRequest, "validation", perr.Error())
			return
		}
		snap, err = a.svc.GetBoardSnapshotWithQuery(r.Context(), id, includeArchived(r), expr)
	} else if includeItems(r) {
		snap, err = a.svc.GetBoardSnapshot(r.Context(), id, includeArchived(r))
	} else {
		snap, err = a.svc.GetBoardSnapshotWithoutItems(r.Context(), id, includeArchived(r))
	}
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// --- columns ---

type createColumnReq struct {
	Name     string  `json:"name"`
	Position *int    `json:"position"`
	Agent    *string `json:"agent"`
}

func (a *API) handleCreateColumn(w http.ResponseWriter, r *http.Request) {
	var req createColumnReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	col, err := a.svc.CreateColumn(r.Context(), id, req.Name, req.Position, service.NormalizeAgentPtr(req.Agent))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, columnResp{Column: col})
}

type updateColumnReq struct {
	Name     *string `json:"name"`
	Position *int    `json:"position"`
	Agent    *string `json:"agent"`
}

func (a *API) handleUpdateColumn(w http.ResponseWriter, r *http.Request) {
	var req updateColumnReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	col, err := a.svc.UpdateColumn(r.Context(), id, req.Name, req.Position, service.NormalizeAgentPtr(req.Agent))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, columnResp{Column: col})
}

// moveColumnReq mirrors MoveInput for items: index 0 = first column, omitted
// = 0, past-the-end clamps.
type moveColumnReq struct {
	Index *int    `json:"index"`
	Agent *string `json:"agent"`
}

func (a *API) handleMoveColumn(w http.ResponseWriter, r *http.Request) {
	var req moveColumnReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	col, err := a.svc.MoveColumn(r.Context(), id, req.Index, service.NormalizeAgentPtr(req.Agent))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, columnResp{Column: col})
}

func (a *API) handleDeleteColumn(w http.ResponseWriter, r *http.Request) {
	var req updateColumnReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if err := a.svc.DeleteColumn(r.Context(), id, service.NormalizeAgentPtr(req.Agent)); err != nil {
		writeServiceErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pathID validates a path id as a UUID (via pgtype.UUID), else 400.
func pathID(w http.ResponseWriter, id string) (string, bool) {
	var u pgtype.UUID
	if u.Scan(id) != nil || !u.Valid {
		writeErr(w, http.StatusBadRequest, "validation", "malformed id "+id)
		return "", false
	}
	return id, true
}

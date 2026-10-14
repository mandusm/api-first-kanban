package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"api-first-kanban/internal/service"
)

// --- items ---

type createItemReq struct {
	Title        string   `json:"title"`
	Description  *string  `json:"description"`
	ColumnID     *string  `json:"column_id"`
	OwnerAgent   *string  `json:"owner_agent"`
	Priority     *int16   `json:"priority"`
	Kind         *string  `json:"kind"`
	ParentItemID *string  `json:"parent_item_id"`
	LabelIDs     []string `json:"label_ids"`
	Agent        *string  `json:"agent"`
}

func (a *API) handleCreateItem(w http.ResponseWriter, r *http.Request) {
	var req createItemReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	item, err := a.svc.CreateItem(r.Context(), id, req.Title, req.Description, req.ColumnID, req.OwnerAgent, req.Priority, req.Kind, req.ParentItemID, req.LabelIDs, service.NormalizeAgentPtr(req.Agent))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"item": item})
}

// handleListBoardItems serves one keyset page of a board column. limit absent
// defaults to service.DefaultPageLimit; limit present outside (0, 500] -> 400.
// column_id is required for v1; malformed/unknown -> 400.
func (a *API) handleListBoardItems(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	limit := service.DefaultPageLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "validation", "limit must be an integer")
			return
		}
		limit = n
	}
	page, err := a.svc.ListBoardItems(r.Context(), id, r.URL.Query().Get("column_id"), limit, r.URL.Query().Get("cursor"), includeArchived(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// includeArchived reads the KISS include_archived query param shared by the
// snapshot and paginated item reads: only the literal "true" opts in.
func includeArchived(r *http.Request) bool {
	return strings.TrimSpace(r.URL.Query().Get("include_archived")) == "true"
}

// includeItems reads the include_items query param of the board snapshot. It
// defaults to true (the historical full payload); only the literal "false"
// opts out into the lightweight snapshot.
func includeItems(r *http.Request) bool {
	return strings.TrimSpace(r.URL.Query().Get("include_items")) != "false"
}

func (a *API) handleGetItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	detail, err := a.svc.GetItem(r.Context(), id)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	// ItemDetail's JSON tags are the response keys (item/comments/relations/
	// children), so the struct is the payload.
	writeJSON(w, http.StatusOK, detail)
}

type updateItemReq struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	OwnerAgent  *string `json:"owner_agent"`
	Priority    *int16  `json:"priority"`
	Kind        *string `json:"kind"`
	// ParentItemID is raw so an explicit null (clear) is distinguishable from an
	// absent field (keep): PATCH three-state semantics.
	ParentItemID json.RawMessage `json:"parent_item_id"`
	LabelIDs     []string        `json:"label_ids"`
	Closed       *bool           `json:"closed"`
	Agent        *string         `json:"agent"`
}

func (a *API) handleUpdateItem(w http.ResponseWriter, r *http.Request) {
	var req updateItemReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	opts := service.UpdateItemOpts{
		Title:       req.Title,
		Description: req.Description,
		OwnerAgent:  req.OwnerAgent,
		Priority:    req.Priority,
		Kind:        req.Kind,
		LabelIDs:    req.LabelIDs,
		Closed:      req.Closed,
	}
	set, pid, err := service.ParseParentItemID(req.ParentItemID)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	opts.ParentSet = set
	opts.ParentItemID = pid
	item, err := a.svc.UpdateItem(r.Context(), id, opts, service.NormalizeAgentPtr(req.Agent))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

type moveItemReq struct {
	ColumnID string  `json:"column_id"`
	Index    *int    `json:"index"`
	Agent    *string `json:"agent"`
}

func (a *API) handleMoveItem(w http.ResponseWriter, r *http.Request) {
	var req moveItemReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if strings.TrimSpace(req.ColumnID) == "" {
		writeErr(w, http.StatusBadRequest, "validation", "column_id is required")
		return
	}
	item, err := a.svc.MoveItem(r.Context(), id, req.ColumnID, req.Index, service.NormalizeAgentPtr(req.Agent))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

type transferItemReq struct {
	ToAgent string  `json:"to_agent"`
	Note    string  `json:"note"`
	Agent   *string `json:"agent"`
}

func (a *API) handleTransferItem(w http.ResponseWriter, r *http.Request) {
	var req transferItemReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	item, err := a.svc.TransferItem(r.Context(), id, req.ToAgent, req.Note, service.NormalizeAgentPtr(req.Agent))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

type archiveItemReq struct {
	Agent *string `json:"agent"`
}

// handleArchiveItem hides an item from default listings (200 {item}); a second
// archive -> 409. Archive is independent of closed.
func (a *API) handleArchiveItem(w http.ResponseWriter, r *http.Request) {
	var req archiveItemReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	item, err := a.svc.ArchiveItem(r.Context(), id, service.NormalizeAgentPtr(req.Agent))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

// handleUnarchiveItem clears the archive state (200 {item}); unarchiving an
// active item -> 409.
func (a *API) handleUnarchiveItem(w http.ResponseWriter, r *http.Request) {
	var req archiveItemReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	item, err := a.svc.UnarchiveItem(r.Context(), id, service.NormalizeAgentPtr(req.Agent))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

// --- relations ---

type createRelationReq struct {
	ToItemID string  `json:"to_item_id"`
	Relation string  `json:"relation"`
	Agent    *string `json:"agent"`
}

func (a *API) handleCreateRelation(w http.ResponseWriter, r *http.Request) {
	var req createRelationReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	to, ok := pathID(w, req.ToItemID)
	if !ok {
		return
	}
	edge, err := a.svc.CreateRelation(r.Context(), id, to, req.Relation, service.NormalizeAgentPtr(req.Agent))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"relation": edge})
}

func (a *API) handleDeleteRelation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agent *string `json:"agent"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if err := a.svc.DeleteRelation(r.Context(), id, service.NormalizeAgentPtr(req.Agent)); err != nil {
		writeServiceErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- comments ---

type addCommentReq struct {
	Body  string  `json:"body"`
	Agent *string `json:"agent"`
}

func (a *API) handleListComments(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	comments, err := a.svc.ListComments(r.Context(), id)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, commentsResp{Comments: comments})
}

func (a *API) handleAddComment(w http.ResponseWriter, r *http.Request) {
	var req addCommentReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	comment, err := a.svc.AddComment(r.Context(), id, req.Body, service.NormalizeAgentPtr(req.Agent))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, commentResp{Comment: comment})
}

// --- labels ---

type createLabelReq struct {
	Name  string  `json:"name"`
	Color *string `json:"color"`
	Agent *string `json:"agent"`
}

func (a *API) handleListLabels(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	// Param presence/shape is decided in the service layer (single truth for
	// REST + MCP). Present-but-empty top/q count as absent (no usage_count leak).
	var top *int
	if tq := r.URL.Query().Get("top"); tq != "" {
		n, err := strconv.Atoi(tq)
		if err != nil || n < 1 {
			writeErr(w, http.StatusBadRequest, "validation", "top must be a positive integer")
			return
		}
		top = &n
	}
	var q *string
	if qq := r.URL.Query().Get("q"); qq != "" {
		q = &qq
	}
	res, err := a.svc.ListLabels(r.Context(), id, q, top)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	if res.Legacy {
		writeJSON(w, http.StatusOK, labelsResp{Labels: res.Labels})
		return
	}
	writeJSON(w, http.StatusOK, labelsRankedResp{Labels: res.Ranked})
}

func (a *API) handleCreateLabel(w http.ResponseWriter, r *http.Request) {
	var req createLabelReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	label, err := a.svc.CreateLabel(r.Context(), id, req.Name, req.Color, service.NormalizeAgentPtr(req.Agent))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, labelResp{Label: label})
}

type updateLabelReq struct {
	Name  *string `json:"name"`
	Color *string `json:"color"`
	Agent *string `json:"agent"`
}

func (a *API) handleUpdateLabel(w http.ResponseWriter, r *http.Request) {
	var req updateLabelReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	label, err := a.svc.UpdateLabel(r.Context(), id, req.Name, req.Color, service.NormalizeAgentPtr(req.Agent))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, labelResp{Label: label})
}

func (a *API) handleDeleteLabel(w http.ResponseWriter, r *http.Request) {
	var req updateLabelReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := pathID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if err := a.svc.DeleteLabel(r.Context(), id, service.NormalizeAgentPtr(req.Agent)); err != nil {
		writeServiceErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

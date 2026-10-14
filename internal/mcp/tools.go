package mcp

import (
	"context"
	"encoding/json"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"api-first-kanban/internal/search"
	"api-first-kanban/internal/service"
)

// addTools registers the kanban tool set. Names, arguments and response shapes
// mirror docs/api.md; every mutating tool accepts an optional "agent" identity.
func addTools(srv *mcpsdk.Server, svc *service.Service) {
	// --- workspaces ---

	addTool(srv, "list_workspaces",
		"List all workspaces.",
		object(map[string]any{}),
		func(ctx context.Context, _ struct{}) (any, error) {
			ws, err := svc.ListWorkspaces(ctx)
			if err != nil {
				return nil, err
			}
			return map[string]any{"workspaces": ws}, nil
		})

	addTool(srv, "create_workspace",
		"Create a workspace. Fails with code \"conflict\" on a duplicate name.",
		object(map[string]any{
			"name":        str("Workspace name (required, unique)."),
			"description": str("Optional description; \"\" is stored as null."),
			"agent":       agentProp,
		}, "name"),
		func(ctx context.Context, in struct {
			Name        string  `json:"name"`
			Description *string `json:"description"`
			Agent       string  `json:"agent"`
		}) (any, error) {
			ws, err := svc.CreateWorkspace(ctx, in.Name, in.Description, service.NormalizeAgent(in.Agent))
			if err != nil {
				return nil, err
			}
			return map[string]any{"workspace": ws}, nil
		})

	// --- boards & columns ---

	addTool(srv, "list_boards",
		"List the boards of a workspace.",
		object(map[string]any{"workspace_id": str("Workspace id (required).")}, "workspace_id"),
		func(ctx context.Context, in struct {
			WorkspaceID string `json:"workspace_id"`
		}) (any, error) {
			boards, err := svc.ListBoards(ctx, in.WorkspaceID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"boards": boards}, nil
		})

	addTool(srv, "create_board",
		"Create a board with initial columns. columns defaults to [\"todo\",\"doing\",\"done\"]. Returns {board:{id,workspace_id,name,key_prefix,columns}}.",
		object(map[string]any{
			"workspace_id": str("Owning workspace id (required)."),
			"name":         str("Board name (required, unique per workspace)."),
			"columns":      arrayOf(str("Column name."), "Initial column names; omitted means todo/doing/done."),
			"agent":        agentProp,
		}, "workspace_id", "name"),
		func(ctx context.Context, in struct {
			WorkspaceID string   `json:"workspace_id"`
			Name        string   `json:"name"`
			Columns     []string `json:"columns"`
			Agent       string   `json:"agent"`
		}) (any, error) {
			snap, err := svc.CreateBoard(ctx, in.WorkspaceID, in.Name, in.Columns, service.NormalizeAgent(in.Agent))
			if err != nil {
				return nil, err
			}
			return map[string]any{"board": map[string]any{
				"id":           snap.Board.ID,
				"workspace_id": snap.Board.WorkspaceID,
				"name":         snap.Board.Name,
				"key_prefix":   snap.Board.KeyPrefix,
				"columns":      snap.Columns,
			}}, nil
		})

	addTool(srv, "get_board",
		"Get a full board snapshot: {board, columns, items}, each item carrying its labels and its derived child-progress counts closed_children/total_children (direct, non-archived children; 0/0 = no indicator), so parent progress is visible without walking children. Archived items are excluded unless include_archived is true. Optional query (search query language, docs/search-query.md, e.g. CREATED < -1d AND LABEL IN ('bug')) filters the snapshot: items contains only matches, per-column item_count reflects the filtered set, and the full composite is returned in one call (no pagination under query). An invalid query fails with code \"validation\" and a message like parse error at 1:1 (offset 0): unexpected input 'created'.",
		object(map[string]any{
			"board_id":         str("Board id (required)."),
			"include_archived": boolean("Include archived items in the snapshot (default false)."),
			"query":            str("Optional search query (spec docs/search-query.md); fields CREATED UPDATED LABEL KIND PRIORITY OWNER TITLE KEY CLOSED with =, !=, <, <=, >, >=, IN, AND/OR/NOT, e.g. \"PRIORITY <= 1 AND LABEL IN ('bug')\". Empty or absent = unfiltered snapshot. Text matching is exact case-insensitive (no substring in v1)."),
		}, "board_id"),
		func(ctx context.Context, in struct {
			BoardID         string `json:"board_id"`
			IncludeArchived bool   `json:"include_archived"`
			Query           string `json:"query"`
		}) (any, error) {
			// Blank (spec §1.1 ASCII whitespace, search.IsBlank) or absent =
			// unfiltered snapshot; a whitespace-only query takes the same path.
			// NBSP-only is NOT blank — it rides the parse path and fails as
			// "unexpected input" per spec §6.
			if search.IsBlank(in.Query) {
				return svc.GetBoardSnapshot(ctx, in.BoardID, in.IncludeArchived)
			}
			// Parse ONCE here (searched-view contract); a *search.ParseError
			// flows through errorResult as the validation envelope verbatim.
			expr, err := search.Parse(in.Query)
			if err != nil {
				return nil, err
			}
			return svc.GetBoardSnapshotWithQuery(ctx, in.BoardID, in.IncludeArchived, expr)
		})

	addTool(srv, "create_column",
		"Add a column to a board. position is omitted to append at the end.",
		object(map[string]any{
			"board_id": str("Board id (required)."),
			"name":     str("Column name (required, unique per board)."),
			"position": integer("Optional integer position; omitted appends."),
			"agent":    agentProp,
		}, "board_id", "name"),
		func(ctx context.Context, in struct {
			BoardID  string `json:"board_id"`
			Name     string `json:"name"`
			Position *int   `json:"position"`
			Agent    string `json:"agent"`
		}) (any, error) {
			col, err := svc.CreateColumn(ctx, in.BoardID, in.Name, in.Position, service.NormalizeAgent(in.Agent))
			if err != nil {
				return nil, err
			}
			return map[string]any{"column": col}, nil
		})

	// --- items ---

	addTool(srv, "create_item",
		"Create an item at the TOP of a column. column_id defaults to the first column; label_ids must belong to the item's workspace. kind is one of milestone, epic, task (default task); parent_item_id must be a milestone for an epic (omit it for a top-level epic) / a milestone or epic for a task. The returned item carries its derived child-progress counts closed_children/total_children (0/0 for a new leaf).",
		object(map[string]any{
			"board_id":       str("Board id (required)."),
			"title":          str("Item title (required)."),
			"description":    str("Optional description."),
			"column_id":      str("Target column id; omitted means the first column."),
			"owner_agent":    str("Optional owning agent."),
			"priority":       integer("Priority 0..3 (P0=blocker, P1=high, P2=normal, P3=low); omitted defaults to 2."),
			"kind":           str("milestone, epic or task; omitted defaults to task."),
			"parent_item_id": str("Parent item id (same workspace): milestone for an epic (omit for a top-level epic), milestone or epic for a task."),
			"label_ids":      arrayOf(str("Label id."), "Label ids to attach (must be in the item's workspace)."),
			"agent":          agentProp,
		}, "board_id", "title"),
		func(ctx context.Context, in struct {
			BoardID      string   `json:"board_id"`
			Title        string   `json:"title"`
			Description  *string  `json:"description"`
			ColumnID     *string  `json:"column_id"`
			OwnerAgent   *string  `json:"owner_agent"`
			Priority     *int16   `json:"priority"`
			Kind         *string  `json:"kind"`
			ParentItemID *string  `json:"parent_item_id"`
			LabelIDs     []string `json:"label_ids"`
			Agent        string   `json:"agent"`
		}) (any, error) {
			item, err := svc.CreateItem(ctx, in.BoardID, in.Title, in.Description, in.ColumnID, in.OwnerAgent, in.Priority, in.Kind, in.ParentItemID, in.LabelIDs, service.NormalizeAgent(in.Agent))
			if err != nil {
				return nil, err
			}
			return map[string]any{"item": item}, nil
		})

	addTool(srv, "get_item",
		"Get an item with its labels, its comments (ascending), its outgoing + incoming relations, and its child items (oldest first; archived children included). The item AND each child row carry the derived child-progress counts closed_children/total_children (direct, non-archived children; 0/0 = no indicator); read them instead of walking children again.",
		object(map[string]any{"item_id": str("Item id (required).")}, "item_id"),
		func(ctx context.Context, in struct {
			ItemID string `json:"item_id"`
		}) (any, error) {
			return svc.GetItem(ctx, in.ItemID)
		})

	addTool(srv, "update_item",
		"Patch an item. Absent fields keep their value; title present but empty is rejected; description:\"\" clears it; parent_item_id null clears the parent; closed:true closes, closed:false reopens. label_ids REPLACES the whole label set when present. The returned item carries its derived child-progress counts closed_children/total_children (direct, non-archived children; 0/0 = no indicator).",
		object(map[string]any{
			"item_id":        str("Item id (required)."),
			"title":          str("New title; omitted keeps, empty is rejected."),
			"description":    str("New description; \"\" clears it, omitted keeps."),
			"owner_agent":    str("New owning agent."),
			"priority":       integer("New priority 0..3 (P0=blocker..P3=low); omitted keeps."),
			"kind":           str("New kind: milestone, epic or task; omitted keeps."),
			"parent_item_id": nullableString("New parent item id; null clears the parent, omitted keeps."),
			"label_ids":      arrayOf(str("Label id."), "Replacement label set ([] clears all labels)."),
			"closed":         boolean("true closes the item, false reopens it."),
			"agent":          agentProp,
		}, "item_id"),
		func(ctx context.Context, in struct {
			ItemID       string          `json:"item_id"`
			Title        *string         `json:"title"`
			Description  *string         `json:"description"`
			OwnerAgent   *string         `json:"owner_agent"`
			Priority     *int16          `json:"priority"`
			Kind         *string         `json:"kind"`
			ParentItemID json.RawMessage `json:"parent_item_id"`
			LabelIDs     []string        `json:"label_ids"`
			Closed       *bool           `json:"closed"`
			Agent        string          `json:"agent"`
		}) (any, error) {
			opts := service.UpdateItemOpts{
				Title:       in.Title,
				Description: in.Description,
				OwnerAgent:  in.OwnerAgent,
				Priority:    in.Priority,
				Kind:        in.Kind,
				LabelIDs:    in.LabelIDs,
				Closed:      in.Closed,
			}
			set, pid, err := service.ParseParentItemID(in.ParentItemID)
			if err != nil {
				return nil, err
			}
			opts.ParentSet = set
			opts.ParentItemID = pid
			item, err := svc.UpdateItem(ctx, in.ItemID, opts, service.NormalizeAgent(in.Agent))
			if err != nil {
				return nil, err
			}
			return map[string]any{"item": item}, nil
		})

	addTool(srv, "move_item",
		"Move an item within its board. index 0 = top (default); an index past the end clamps to the end.",
		object(map[string]any{
			"item_id":   str("Item id (required)."),
			"column_id": str("Target column id (required, same board)."),
			"index":     integer("Target slot; 0 = top, omitted = 0."),
			"agent":     agentProp,
		}, "item_id", "column_id"),
		func(ctx context.Context, in struct {
			ItemID   string `json:"item_id"`
			ColumnID string `json:"column_id"`
			Index    *int   `json:"index"`
			Agent    string `json:"agent"`
		}) (any, error) {
			item, err := svc.MoveItem(ctx, in.ItemID, in.ColumnID, in.Index, service.NormalizeAgent(in.Agent))
			if err != nil {
				return nil, err
			}
			return map[string]any{"item": item}, nil
		})

	addTool(srv, "move_column",
		"Reorder a column on its board. index 0 = first column (default); an index past the end clamps to the end.",
		object(map[string]any{
			"column_id": str("Column id (required)."),
			"index":     integer("Target slot; 0 = first, omitted = 0."),
			"agent":     agentProp,
		}, "column_id"),
		func(ctx context.Context, in struct {
			ColumnID string `json:"column_id"`
			Index    *int   `json:"index"`
			Agent    string `json:"agent"`
		}) (any, error) {
			col, err := svc.MoveColumn(ctx, in.ColumnID, in.Index, service.NormalizeAgent(in.Agent))
			if err != nil {
				return nil, err
			}
			return map[string]any{"column": col}, nil
		})

	addTool(srv, "transfer_item",
		"Transfer an item to another agent. Sets owner_agent and auto-posts a \"[handoff] <from> → <to> — <note>\" comment.",
		object(map[string]any{
			"item_id":  str("Item id (required)."),
			"to_agent": str("Receiving agent (required)."),
			"note":     str("Optional handoff note."),
			"agent":    agentProp,
		}, "item_id", "to_agent"),
		func(ctx context.Context, in struct {
			ItemID  string `json:"item_id"`
			ToAgent string `json:"to_agent"`
			Note    string `json:"note"`
			Agent   string `json:"agent"`
		}) (any, error) {
			item, err := svc.TransferItem(ctx, in.ItemID, in.ToAgent, in.Note, service.NormalizeAgent(in.Agent))
			if err != nil {
				return nil, err
			}
			return map[string]any{"item": item}, nil
		})

	addTool(srv, "archive_item",
		"Archive an item so it disappears from default board snapshots and pages (item.archived_at becomes non-null). Archive is independent of closed and never cascades to children or relations. Archiving an already-archived item fails with code \"conflict\".",
		object(map[string]any{
			"item_id": str("Item id (required)."),
			"agent":   agentProp,
		}, "item_id"),
		func(ctx context.Context, in struct {
			ItemID string `json:"item_id"`
			Agent  string `json:"agent"`
		}) (any, error) {
			item, err := svc.ArchiveItem(ctx, in.ItemID, service.NormalizeAgent(in.Agent))
			if err != nil {
				return nil, err
			}
			return map[string]any{"item": item}, nil
		})

	addTool(srv, "unarchive_item",
		"Unarchive an item so it returns to default board snapshots and pages (item.archived_at becomes null). Unarchiving an item that is not archived fails with code \"conflict\".",
		object(map[string]any{
			"item_id": str("Item id (required)."),
			"agent":   agentProp,
		}, "item_id"),
		func(ctx context.Context, in struct {
			ItemID string `json:"item_id"`
			Agent  string `json:"agent"`
		}) (any, error) {
			item, err := svc.UnarchiveItem(ctx, in.ItemID, service.NormalizeAgent(in.Agent))
			if err != nil {
				return nil, err
			}
			return map[string]any{"item": item}, nil
		})

	addTool(srv, "add_relation",
		"Create a directed relation from one item to another (same workspace). relation is one of blocks, relates_to, duplicates; (A blocks B) means B is blocked by A. Self-relations and exact duplicates are rejected.",
		object(map[string]any{
			"from_item_id": str("Source item id (required)."),
			"to_item_id":   str("Target item id (required, same workspace)."),
			"relation":     str("One of blocks, relates_to, duplicates (required)."),
			"agent":        agentProp,
		}, "from_item_id", "to_item_id", "relation"),
		func(ctx context.Context, in struct {
			FromItemID string `json:"from_item_id"`
			ToItemID   string `json:"to_item_id"`
			Relation   string `json:"relation"`
			Agent      string `json:"agent"`
		}) (any, error) {
			edge, err := svc.CreateRelation(ctx, in.FromItemID, in.ToItemID, in.Relation, service.NormalizeAgent(in.Agent))
			if err != nil {
				return nil, err
			}
			return map[string]any{"relation": edge}, nil
		})

	addTool(srv, "remove_relation",
		"Delete a relation by id.",
		object(map[string]any{
			"relation_id": str("Relation id (required)."),
			"agent":       agentProp,
		}, "relation_id"),
		func(ctx context.Context, in struct {
			RelationID string `json:"relation_id"`
			Agent      string `json:"agent"`
		}) (any, error) {
			if err := svc.DeleteRelation(ctx, in.RelationID, service.NormalizeAgent(in.Agent)); err != nil {
				return nil, err
			}
			return map[string]any{"deleted": true}, nil
		})

	// --- labels ---

	addTool(srv, "list_labels",
		"List the labels of a workspace. With top (positive int) or a non-blank query (case-insensitive name substring, applied BEFORE ranking/filter-then-rank) the response is ranked by USAGE (number of items carrying each label; ties by name asc) and each label carries usage_count; top truncates to the most-used N. With neither param (and no blank query) the response is the legacy shape: all labels name-ordered, no usage_count (same as the REST no-param response). When picking labels for items, DEFAULT to top (e.g. 10) instead of dumping all labels, and use query type-to-search when the needed label is not in the short list; search before creating a new label (duplicate name fails with \"conflict\").",
		object(map[string]any{
			"workspace_id": str("Workspace id (required)."),
			"top":          integer("Optional positive integer; return only the N most-used labels (usage desc, ties by name)."),
			"query":        str("Optional case-insensitive name substring filter, applied before ranking (e.g. \"bug\")."),
		}, "workspace_id"),
		func(ctx context.Context, in struct {
			WorkspaceID string  `json:"workspace_id"`
			Top         *int    `json:"top"`
			Query       *string `json:"query"`
		}) (any, error) {
			// The legacy-vs-ranked branch lives in the service layer: with no
			// params this returns the same legacy shape as the REST handler.
			res, err := svc.ListLabels(ctx, in.WorkspaceID, in.Query, in.Top)
			if err != nil {
				return nil, err
			}
			if res.Legacy {
				return map[string]any{"labels": res.Labels}, nil
			}
			return map[string]any{"labels": res.Ranked}, nil
		})

	addTool(srv, "create_label",
		"Create a workspace-scoped label. color defaults to #6366f1; duplicate name fails with code \"conflict\". Attach/detach via create_item/update_item label_ids.",
		object(map[string]any{
			"workspace_id": str("Workspace id (required)."),
			"name":         str("Label name (required, unique per workspace)."),
			"color":        str("Hex color; default #6366f1."),
			"agent":        agentProp,
		}, "workspace_id", "name"),
		func(ctx context.Context, in struct {
			WorkspaceID string  `json:"workspace_id"`
			Name        string  `json:"name"`
			Color       *string `json:"color"`
			Agent       string  `json:"agent"`
		}) (any, error) {
			label, err := svc.CreateLabel(ctx, in.WorkspaceID, in.Name, in.Color, service.NormalizeAgent(in.Agent))
			if err != nil {
				return nil, err
			}
			return map[string]any{"label": label}, nil
		})

	// --- comments ---

	addTool(srv, "list_comments",
		"List an item's comments in ascending order.",
		object(map[string]any{"item_id": str("Item id (required).")}, "item_id"),
		func(ctx context.Context, in struct {
			ItemID string `json:"item_id"`
		}) (any, error) {
			comments, err := svc.ListComments(ctx, in.ItemID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"comments": comments}, nil
		})

	addTool(srv, "add_comment",
		"Add a comment to an item.",
		object(map[string]any{
			"item_id": str("Item id (required)."),
			"body":    str("Comment body (required)."),
			"agent":   agentProp,
		}, "item_id", "body"),
		func(ctx context.Context, in struct {
			ItemID string `json:"item_id"`
			Body   string `json:"body"`
			Agent  string `json:"agent"`
		}) (any, error) {
			comment, err := svc.AddComment(ctx, in.ItemID, in.Body, service.NormalizeAgent(in.Agent))
			if err != nil {
				return nil, err
			}
			return map[string]any{"comment": comment}, nil
		})
}

// --- JSON schema helpers (draft 2020-12, type: object) ---

var agentProp = str("Caller identity recorded on events; defaults to \"human\".")

func object(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

// nullableString marks a field that accepts a JSON null (e.g. update_item's
// three-state parent_item_id, where null clears the value).
func nullableString(desc string) map[string]any {
	return map[string]any{"type": []string{"string", "null"}, "description": desc}
}

func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func arrayOf(items map[string]any, desc string) map[string]any {
	return map[string]any{"type": "array", "items": items, "description": desc}
}

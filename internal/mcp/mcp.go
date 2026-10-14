// Package mcp mounts the MCP (Model Context Protocol) endpoint on the API
// server. It is a THIN adapter over internal/service: every tool call runs the
// same domain logic as the REST handlers, so mutations keep emitting the same
// events + pg_notify exactly as the REST path does. Tool names, input schemas
// and response shapes mirror docs/api.md.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"api-first-kanban/internal/search"
	"api-first-kanban/internal/service"
)

// serverVersion is reported in the MCP initialize handshake.
const serverVersion = "1.0.0"

// instructions carry the agent guidance required by the project charter. It is
// returned to every client on initialize.
const instructions = `api-first-kanban is an agent-first kanban service. Work is organised into workspaces -> boards -> columns -> items, with workspace-scoped labels and per-item comments.

Discovery workflow: list_workspaces, then list_boards, then get_board (a full snapshot of board + columns + items, each item carrying its labels; archived items excluded unless include_archived is true). Discover state before mutating.

Searching: get_board accepts an optional "query" in the search query language (docs/search-query.md, e.g. "CREATED < -1d AND LABEL IN ('bug')"). A non-empty query returns the full board composite with only the matching items; per-column item_count reflects the filtered set; archived items are excluded unless include_archived is true. An empty or absent query means today's unfiltered snapshot. An invalid query is a tool error with code "validation" whose message is "parse error at <line>:<col> (offset <n>): <description>" — show it to the caller verbatim.

Tool semantics:
- update_item uses PATCH semantics: an ABSENT field keeps its current value. An empty title/name when present is rejected. description:"" clears the description. closed:true closes the item, closed:false reopens it; items are closed, never deleted.
- archive_item hides an item from default board snapshots and pages (archived_at becomes a non-null timestamp); unarchive_item restores it (archived_at null). Archiving is independent of closed, never cascades to the item's children or relations, and a repeat archive (or an unarchive of an active item) fails with code "conflict". The board snapshot (get_board) EXCLUDES archived items unless include_archived is true; item payloads always carry archived_at.
- create_item puts the item at the TOP of the target column; column_id defaults to the first column.
- move_item moves an item within its own board; index 0 = top (default), an index past the end clamps to the end.
- move_column reorders a column on its board; index 0 = first (default), an index past the end clamps to the end. Column positions are renumbered densely 0..n-1.
- transfer_item sets owner_agent and auto-posts a "[handoff] <from> → <to> — <note>" comment.
- add_comment appends a comment to an item; list_comments reads them back in ascending order. Comment bodies are stored as raw Markdown source and rendered as sanitized Markdown in the web detail view, so agents may use Markdown formatting (headings, lists, code, links) when writing comments.
- add_relation creates a directed link between two items in the SAME workspace (relation is blocks, relates_to or duplicates); remove_relation deletes it by id. (A blocks B) means B is blocked by A — "blocked by" is the derived INCOMING view, not a stored value. Self-relations and exact duplicates are rejected.
- label_ids on create_item/update_item REPLACE the item's full label set; every label must belong to the item's workspace.

Priority: every item carries a priority on the P0..P3 scale (0=P0 blocker, 1=P1 high, 2=P2 normal, 3=P3 low). It defaults to P2 (2) when omitted and is echoed on every item and board snapshot; a value outside 0..3 is rejected.

Child progress: every item carries closed_children/total_children — the counts of its DIRECT children that are closed / total. Archived children are excluded from BOTH counts, children may live on another board of the same workspace, and 0/0 means "no indicator". Use them to read an epic's or milestone's progress without walking its children; there is no percent field, so derive closed/total yourself. Nothing auto-closes a parent when its children finish, so a milestone can read low while its descendant tasks are done. Subtree progress: every item also carries closed_descendants/total_descendants — a RECURSIVE roll-up over ALL non-archived descendants at any depth (milestone -> epic -> task), archived descendants excluded from both counts, leaves read 0/0; with only direct children they equal the direct pair, so a milestone can read its full nested progress from these two fields without walking the tree. Activity: every item carries activity_at — the later of updated_at and the newest comment's created_at (comments do not bump updated_at), so use it for recency views, not change detection.

Hierarchy: every item has a kind (milestone, epic or task; default task) and an optional parent_item_id. The chain is milestone -> epic -> task and is at most three levels deep; that kind/parent-kind rule caps the depth, so a valid hierarchy cannot contain a cycle, and the server rejects any parent chain that would reach the item itself. A milestone has no parent; an epic's parent, when present, must be a milestone (a parentless epic is a valid top-level item); a task's parent may be an epic or a milestone (a task with no parent is top-level). The parent must be in the SAME workspace. On update_item, parent_item_id absent keeps the parent and an explicit null clears it (rejected when the resulting kind/parent combination would be invalid). get_item returns the item's children (oldest first, archived included) so a parent's sub-items and their statuses can be read in one call.

Positions: "position" is a decimal STRING carrying a fractional midpoint (e.g. "1", "0.5", "0.75"). Parse it NUMERICALLY, never lexically. Ties are broken by created_at. Equal positions only occur on a real collision and are silently renormalized to 1..n.

Identity: pass "agent" (a plain string, default "human") on every mutation; it is recorded on items, comments and events.

Responses mirror the REST API: single resources are wrapped ("workspace", "board", "column", "item", "label", "comment"), lists are plural ("workspaces", "boards", "labels", "comments"), and errors are {"error":{"code","message"}}.`

// Register mounts the stateless Streamable HTTP MCP endpoint at /mcp on mux,
// on the same server as the REST API.
func Register(mux *http.ServeMux, svc *service.Service) {
	srv := NewServer(svc)
	handler := mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return srv },
		&mcpsdk.StreamableHTTPOptions{Stateless: true},
	)
	mux.Handle("/mcp", handler)
}

// NewServer builds the MCP server with the kanban tool set registered. Each
// request is served by the same server: the tools are pure functions of the
// service, so the stateless transport needs no per-session state.
func NewServer(svc *service.Service) *mcpsdk.Server {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "api-first-kanban",
		Version: serverVersion,
	}, &mcpsdk.ServerOptions{Instructions: instructions})
	addTools(srv, svc)
	return srv
}

// toolFunc is a domain call that returns a JSON-marshalable result mirroring a
// REST response body.
type toolFunc[In any] func(ctx context.Context, in In) (any, error)

// addTool registers a low-level tool whose handler unmarshals the raw
// arguments into In, runs fn, and wraps the outcome as MCP content. Domain
// errors become tool errors (IsError) carrying the REST error envelope; the
// tool list never contains MCP protocol-level failures.
func addTool[In any](srv *mcpsdk.Server, name, description string, inputSchema map[string]any, fn toolFunc[In]) {
	srv.AddTool(&mcpsdk.Tool{Name: name, Description: description, InputSchema: inputSchema},
		func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			var in In
			if len(req.Params.Arguments) > 0 {
				if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
					return errorEnvelope("validation", "invalid arguments: "+err.Error()), nil
				}
			}
			out, err := fn(ctx, in)
			if err != nil {
				return errorResult(err), nil
			}
			return successResult(out)
		})
}

// successResult renders out as JSON text plus structured content.
func successResult(out any) (*mcpsdk.CallToolResult, error) {
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &mcpsdk.CallToolResult{
		Content:           []mcpsdk.Content{&mcpsdk.TextContent{Text: string(b)}},
		StructuredContent: out,
	}, nil
}

// errorResult maps a service sentinel error onto the REST error envelope,
// exposed as an MCP tool error so the model can see and self-correct.
func errorResult(err error) *mcpsdk.CallToolResult {
	code, msg := "internal", "internal error"
	// Search parse errors (spec §6) carry their own formatted message
	// ("parse error at L:C (offset N): ...") and map to validation, exactly
	// like the REST q-parameter mapping on PB-58.
	var pe *search.ParseError
	if errors.As(err, &pe) {
		return errorEnvelope("validation", pe.Error())
	}
	var se *service.ServiceError
	if errors.As(err, &se) {
		switch se.Sentinel() {
		case service.ErrInvalid:
			code = "validation"
		case service.ErrNotFound:
			code = "not_found"
		case service.ErrConflict:
			code = "conflict"
		case service.ErrForbidden:
			code = "forbidden"
		}
		msg = se.Message()
	} else {
		slog.Error("mcp tool call failed", "err", err)
	}
	return errorEnvelope(code, msg)
}

func errorEnvelope(code, msg string) *mcpsdk.CallToolResult {
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": msg}})
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(b)}},
	}
}

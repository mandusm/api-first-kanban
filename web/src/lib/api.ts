// Typed client for the api-first-kanban API v1.
//
// One function per endpoint, one shared error path. In dev the Vite proxy
// forwards /api to the Go API; set VITE_API_URL to point at another origin.

import type {
  Board,
  BoardInput,
  BoardItemsPage,
  BoardSnapshot,
  Column,
  ColumnCreateInput,
  ColumnMoveInput,
  ColumnPatchInput,
  Comment,
  CommentInput,
  CreatedBoard,
  Health,
  Item,
  ItemCreateInput,
  ItemDetail,
  ItemPatchInput,
  Label,
  LabelInput,
  LabelPatchInput,
  MoveInput,
  Relation,
  RelationInput,
  TransferInput,
  Workspace,
  WorkspaceInput,
} from './types'

/** Every UI-initiated mutation is attributed to the human operator. */
export const DEFAULT_AGENT = 'human'

/** Base URL for API calls: '' (same origin, via dev proxy) unless overridden. */
export function apiBase(): string {
  const override = import.meta.env.VITE_API_URL
  return typeof override === 'string' && override.length > 0 ? override.replace(/\/+$/, '') : ''
}

export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

/** Human-readable message for any thrown value. */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message
  if (err instanceof Error) return err.message
  return String(err)
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  const init: RequestInit = { method, headers }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(body)
  }

  let res: Response
  try {
    res = await fetch(`${apiBase()}${path}`, init)
  } catch (cause) {
    // Network/connection failure — distinct code so callers can special-case it.
    throw new ApiError(0, 'network_error', cause instanceof Error ? cause.message : String(cause))
  }

  if (res.status === 204) return undefined as T

  const text = await res.text()
  let parsed: unknown = null
  if (text.length > 0) {
    try {
      parsed = JSON.parse(text)
    } catch {
      parsed = null
    }
  }

  if (!res.ok) {
    const body = (parsed as { error?: { code?: string; message?: string } } | null)?.error
    throw new ApiError(res.status, body?.code ?? `http_${res.status}`, body?.message ?? res.statusText)
  }

  return parsed as T
}

/**
 * Unwraps a single-resource mutation response.
 *
 * RULED CONVENTION: every single-resource mutation response is wrapped under
 * the entity's singular key (`{workspace}`/`{board}`/`{column}`/`{item}`/
 * `{label}`/`{comment}`) — the same convention as the SSE envelope. This is
 * deliberately STRICT: if the server ever returns a bare entity, this throws
 * rather than silently adapting, so contract drift surfaces immediately.
 */
function unwrapEntity<T extends object>(res: unknown, key: string, path: string): T {
  if (res === null || typeof res !== 'object' || !(key in (res as Record<string, unknown>))) {
    throw new ApiError(0, 'malformed_response', `expected an object with key "${key}" from ${path}`)
  }
  return (res as Record<string, T>)[key]
}

async function mutateEntity<T extends object>(
  method: string,
  path: string,
  key: string,
  body?: unknown,
): Promise<T> {
  return unwrapEntity<T>(await request<unknown>(method, path, body), key, path)
}

// --- health ---------------------------------------------------------------

export const getHealth = () => request<Health>('GET', '/api/healthz')

// --- workspaces -----------------------------------------------------------

export const listWorkspaces = () => request<{ workspaces: Workspace[] }>('GET', '/api/workspaces')

export const createWorkspace = (input: WorkspaceInput) =>
  mutateEntity<Workspace>('POST', '/api/workspaces', 'workspace', input)

// --- boards ---------------------------------------------------------------

export const listBoards = (workspaceId: string) =>
  request<{ boards: Board[] }>('GET', `/api/workspaces/${workspaceId}/boards`)

export const createBoard = (workspaceId: string, input: BoardInput) =>
  mutateEntity<CreatedBoard>('POST', `/api/workspaces/${workspaceId}/boards`, 'board', input)

export const getBoard = (
  boardId: string,
  params: { includeArchived?: boolean; includeItems?: boolean; q?: string } = {},
) => {
  const query = new URLSearchParams()
  if (params.includeArchived) query.set('include_archived', 'true')
  // include_items defaults to true server-side; only the literal false opts out
  // of walking the items table (the lightweight board+columns+counts read).
  if (params.includeItems === false) query.set('include_items', 'false')
  // Server-side search (search-query v1). Omitted entirely for a blank query so
  // the request is byte-identical to the pre-search board read.
  if (params.q) query.set('q', params.q)
  const qs = query.toString()
  return request<BoardSnapshot>('GET', `/api/boards/${boardId}${qs ? `?${qs}` : ''}`)
}

/**
 * One keyset page of a single column (KB-16 endpoint). `cursor` is the opaque
 * `next_cursor` from the previous page; omit it for the first page. `column_id`
 * is required in v1.
 */
export function listBoardItems(
  boardId: string,
  params: {
    column_id: string
    limit?: number
    cursor?: string
    includeArchived?: boolean
  },
) {
  const query = new URLSearchParams({ column_id: params.column_id })
  if (params.limit !== undefined) query.set('limit', String(params.limit))
  if (params.cursor !== undefined) query.set('cursor', params.cursor)
  if (params.includeArchived) query.set('include_archived', 'true')
  return request<BoardItemsPage>('GET', `/api/boards/${boardId}/items?${query.toString()}`)
}

// --- columns --------------------------------------------------------------

export const createColumn = (boardId: string, input: ColumnCreateInput) =>
  mutateEntity<Column>('POST', `/api/boards/${boardId}/columns`, 'column', input)

export const updateColumn = (columnId: string, input: ColumnPatchInput) =>
  mutateEntity<Column>('PATCH', `/api/columns/${columnId}`, 'column', input)

export const deleteColumn = (columnId: string) =>
  request<void>('DELETE', `/api/columns/${columnId}`)

export const moveColumn = (columnId: string, input: ColumnMoveInput) =>
  mutateEntity<Column>('POST', `/api/columns/${columnId}/move`, 'column', input)

// --- items ----------------------------------------------------------------

export const createItem = (boardId: string, input: ItemCreateInput) =>
  mutateEntity<Item>('POST', `/api/boards/${boardId}/items`, 'item', input)

export const getItem = (itemId: string) => request<ItemDetail>('GET', `/api/items/${itemId}`)

export const updateItem = (itemId: string, input: ItemPatchInput) =>
  mutateEntity<Item>('PATCH', `/api/items/${itemId}`, 'item', input)

export const moveItem = (itemId: string, input: MoveInput) =>
  mutateEntity<Item>('POST', `/api/items/${itemId}/move`, 'item', input)

export const transferItem = (itemId: string, input: TransferInput) =>
  mutateEntity<Item>('POST', `/api/items/${itemId}/transfer`, 'item', input)

/** Archive an item ({agent?} body); 409 if it is already archived. */
export const archiveItem = (itemId: string, agent?: string) =>
  mutateEntity<Item>('POST', `/api/items/${itemId}/archive`, 'item', agent ? { agent } : {})

/** Unarchive an item ({agent?} body); 409 if it is not archived. */
export const unarchiveItem = (itemId: string, agent?: string) =>
  mutateEntity<Item>('POST', `/api/items/${itemId}/unarchive`, 'item', agent ? { agent } : {})

// --- labels ---------------------------------------------------------------

export const listLabels = (workspaceId: string, params?: { top?: number; q?: string }) => {
  const search = new URLSearchParams()
  if (params?.top != null) search.set('top', String(params.top))
  if (params?.q) search.set('q', params.q)
  const qs = search.toString()
  return request<{ labels: Label[] }>(
    'GET',
    `/api/workspaces/${workspaceId}/labels${qs.length > 0 ? `?${qs}` : ''}`,
  )
}

export const createLabel = (workspaceId: string, input: LabelInput) =>
  mutateEntity<Label>('POST', `/api/workspaces/${workspaceId}/labels`, 'label', input)

export const updateLabel = (labelId: string, input: LabelPatchInput) =>
  mutateEntity<Label>('PATCH', `/api/labels/${labelId}`, 'label', input)

export const deleteLabel = (labelId: string) => request<void>('DELETE', `/api/labels/${labelId}`)

// --- comments -------------------------------------------------------------

export const listComments = (itemId: string) =>
  request<{ comments: Comment[] }>('GET', `/api/items/${itemId}/comments`)

export const createComment = (itemId: string, input: CommentInput) =>
  mutateEntity<Comment>('POST', `/api/items/${itemId}/comments`, 'comment', input)

// --- relations ------------------------------------------------------------

/** Create a relation FROM `itemId` to `input.to_item_id`. */
export const createRelation = (itemId: string, input: RelationInput) =>
  mutateEntity<Relation>('POST', `/api/items/${itemId}/relations`, 'relation', input)

export const deleteRelation = (relationId: string) =>
  request<void>('DELETE', `/api/relations/${relationId}`)

// --- SSE ------------------------------------------------------------------

/** URL for GET /api/events (SSE), optionally scoped to a board or workspace. */
export function eventsUrl(params: { boardId?: string; workspaceId?: string }): string {
  const query = new URLSearchParams()
  if (params.boardId) query.set('board_id', params.boardId)
  if (params.workspaceId) query.set('workspace_id', params.workspaceId)
  const qs = query.toString()
  return `${apiBase()}/api/events${qs ? `?${qs}` : ''}`
}

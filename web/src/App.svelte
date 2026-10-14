<script lang="ts">
  import BoardView from './lib/BoardView.svelte'
  import WorkspaceSelector from './lib/WorkspaceSelector.svelte'
  import { kanban } from './lib/kanban.svelte'
  import { getHealth } from './lib/api'
  import { EventStream } from './lib/sse'
  import { onMount } from 'svelte'

  let stream: EventStream | null = null

  // Build identity of the API this page is talking to, shown in the footer so
  // "stale server" is visible without shelling out (make check-deployed).
  let commit = $state('')
  const shortCommit = $derived(
    commit.endsWith('-dirty') ? commit.slice(0, 7) + '-dirty' : commit.slice(0, 7),
  )

  const connection = $derived(
    kanban.live ? 'live' : kanban.reconnecting ? 'reconnecting' : 'connecting',
  )

  onMount(() => {
    void kanban.loadWorkspaces()
    void getHealth()
      .then((health) => {
        commit = health.commit ?? ''
      })
      .catch(() => {
        /* non-fatal: the footer simply shows no build id */
      })
    return () => {
      stream?.close()
      stream = null
    }
  })

  // One stream per open board: reconnect + board switch both rebuild it.
  $effect(() => {
    const boardId = kanban.boardId
    stream?.close()
    stream = null
    if (!boardId) return

    const next = new EventStream({
      boardId,
      onEvent: (event) => kanban.applyEvent(event),
      // Resync after every (re)open: covers anything missed while down.
      onOpen: () => void kanban.refresh(),
      onStatus: (status) => {
        kanban.live = status === 'open'
        kanban.reconnecting = status === 'reconnecting' || status === 'connecting'
      },
    })
    stream = next
    next.start()
  })
</script>

<main class="mx-auto min-h-svh max-w-[1400px] px-6 py-6 font-sans text-slate-800">
  <header class="mb-6 flex flex-wrap items-center justify-between gap-x-6 gap-y-3">
    <div class="flex items-center gap-3">
      <h1 class="text-xl font-semibold text-slate-900">api-first-kanban</h1>
      {#if kanban.boardId}
        <span
          class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-medium"
          class:bg-emerald-100={connection === 'live'}
          class:text-emerald-700={connection === 'live'}
          class:bg-amber-100={connection !== 'live'}
          class:text-amber-700={connection !== 'live'}
        >
          <span
            class="h-1.5 w-1.5 rounded-full"
            class:bg-emerald-500={connection === 'live'}
            class:bg-amber-500={connection !== 'live'}
          ></span>
          {connection}
        </span>
      {/if}
    </div>

    <label class="order-3 flex w-full min-w-[16rem] flex-1 items-center justify-center gap-1.5 text-xs text-slate-500 md:order-none md:w-auto md:flex-initial">
      <span class="sr-only">Search</span>
      <input
        type="search"
        data-search-input
        class="h-7 w-full max-w-md rounded-full border bg-white px-3 text-xs font-medium text-slate-700 placeholder:text-slate-400 focus:outline-none {kanban.searchError
          ? 'border-rose-400 focus:border-rose-500'
          : 'border-slate-300 focus:border-slate-500'}"
        placeholder="CREATED < -1d AND LABEL IN ('bug')"
        aria-label="Search items"
        aria-invalid={kanban.searchError ? 'true' : undefined}
        title="Filter items server-side with the search query language (docs/search-query.md)"
        value={kanban.searchInput}
        oninput={(event) => kanban.setSearchInput(event.currentTarget.value)}
      />
    </label>

    <WorkspaceSelector store={kanban} />
  </header>

  {#if kanban.searchError}
    <p class="mb-3 text-[11px] text-rose-700" data-search-error role="alert">
      {kanban.searchError}
    </p>
  {/if}

  {#if kanban.error}
    <div class="mb-5 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-rose-300 bg-rose-50 px-4 py-3 text-sm text-rose-800">
      <p>
        <span class="font-semibold">Could not reach the API.</span>
        {kanban.error}
      </p>
      <button
        class="rounded-md border border-rose-300 px-3 py-1 font-medium hover:bg-rose-100"
        onclick={() => void kanban.loadWorkspaces()}
      >
        Retry
      </button>
    </div>
  {/if}

  {#if kanban.actionError}
    <div class="mb-5 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 text-sm text-amber-900">
      <p><span class="font-semibold">Action failed.</span> {kanban.actionError}</p>
      <button
        class="rounded-md border border-amber-300 px-3 py-1 font-medium hover:bg-amber-100"
        onclick={() => kanban.clearActionError()}
      >
        Dismiss
      </button>
    </div>
  {/if}

  {#if kanban.loading && !kanban.board}
    <p class="rounded-lg border border-slate-200 bg-white px-4 py-8 text-center text-sm text-slate-500">
      Loading board…
    </p>
  {:else if kanban.board}
    <BoardView store={kanban} />
  {:else if !kanban.error}
    <div class="rounded-lg border border-dashed border-slate-300 p-8 text-center text-sm text-slate-500">
      {#if kanban.workspaces.length === 0}
        <p class="font-medium text-slate-700">No workspaces yet.</p>
        <p class="mt-1">
          Create one through the API (<code class="font-mono text-xs">POST /api/workspaces</code>) and
          it will appear here.
        </p>
      {:else}
        <p class="font-medium text-slate-700">This workspace has no boards yet.</p>
        <p class="mt-1">Create one with <code class="font-mono text-xs">POST /api/workspaces/{kanban.workspaceId}/boards</code>.</p>
      {/if}
    </div>
  {/if}

  <footer class="mt-8 flex items-center justify-between border-t border-slate-200 pt-3 text-[11px] text-slate-400">
    <span>M3b — add items, move, transfer, edit, close/reopen and comment as human. Live updates over SSE.</span>
    {#if commit}
      <span class="font-mono" title="API build commit (make check-deployed)">{shortCommit}</span>
    {/if}
  </footer>
</main>

<script lang="ts">
  import type { KanbanStore } from './kanban.svelte'

  let { store }: { store: KanbanStore } = $props()
</script>

<div class="flex flex-wrap items-center gap-x-4 gap-y-2">
  <div class="flex items-center gap-2">
    <label class="text-[11px] font-semibold uppercase tracking-wide text-slate-400" for="ws-select">
      Workspace
    </label>
    <select
      id="ws-select"
      class="max-w-52 rounded-md border border-slate-300 bg-white px-2 py-1 text-sm text-slate-800 disabled:opacity-50"
      disabled={store.workspaces.length === 0}
      value={store.workspaceId ?? ''}
      onchange={(event) => void store.selectWorkspace(event.currentTarget.value)}
    >
      {#if store.workspaces.length === 0}
        <option value="">none</option>
      {/if}
      {#each store.workspaces as workspace (workspace.id)}
        <option value={workspace.id}>{workspace.name}</option>
      {/each}
    </select>
  </div>

  <div class="flex items-center gap-2">
    <label class="text-[11px] font-semibold uppercase tracking-wide text-slate-400" for="board-select">
      Board
    </label>
    <select
      id="board-select"
      class="max-w-52 rounded-md border border-slate-300 bg-white px-2 py-1 text-sm text-slate-800 disabled:opacity-50"
      disabled={store.boards.length === 0}
      value={store.boardId ?? ''}
      onchange={(event) => void store.selectBoard(event.currentTarget.value)}
    >
      {#if store.boards.length === 0}
        <option value="">none</option>
      {/if}
      {#each store.boards as board (board.id)}
        <option value={board.id}>{board.name}</option>
      {/each}
    </select>
  </div>

  <button
    class="rounded-md border border-slate-300 px-2.5 py-1 text-sm font-medium text-slate-700 hover:bg-slate-100 disabled:opacity-50"
    onclick={() => void store.loadWorkspaces()}
    disabled={store.loading}
  >
    Refresh
  </button>
</div>

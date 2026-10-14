<script lang="ts">
  import type { KanbanStore } from './kanban.svelte'
  import type { Item } from './types'

  let {
    item,
    store,
    onClose,
  }: { item: Item; store: KanbanStore; onClose: () => void } = $props()

  let dialog = $state<HTMLDialogElement | null>(null)
  let toAgent = $state('')
  let note = $state('')
  let error = $state<string | null>(null)

  $effect(() => {
    if (dialog && !dialog.open) dialog.showModal()
  })

  async function submit(event: SubmitEvent) {
    event.preventDefault()
    const target = toAgent.trim()
    if (target.length === 0) {
      error = 'Enter the agent to hand off to.'
      return
    }
    const ok = await store.transferItem(item.id, target, note.trim() || undefined)
    if (ok) onClose()
    else error = store.actionError
  }
</script>

<dialog
  bind:this={dialog}
  onclose={onClose}
  class="w-96 max-w-[92vw] rounded-lg border border-slate-200 p-0 shadow-lg backdrop:bg-slate-900/40"
>
  <form onsubmit={submit} class="flex flex-col gap-3 p-4">
    <div>
      <h2 class="text-sm font-semibold text-slate-900">Transfer item</h2>
      <p class="mt-0.5 text-xs text-slate-500">{item.title}</p>
    </div>

    <label class="flex flex-col gap-1 text-xs font-medium text-slate-600">
      To agent
      <input
        class="rounded-md border border-slate-300 px-2 py-1 text-sm font-normal text-slate-800"
        bind:value={toAgent}
        placeholder="e.g. be-1"
      />
    </label>

    <label class="flex flex-col gap-1 text-xs font-medium text-slate-600">
      Note (optional)
      <textarea
        class="rounded-md border border-slate-300 px-2 py-1 text-sm font-normal text-slate-800"
        bind:value={note}
        rows="2"
        placeholder="Context for the hand-off"
      ></textarea>
    </label>

    {#if error}
      <p class="text-xs text-rose-700">{error}</p>
    {/if}

    <p class="text-[11px] text-slate-400">
      A <code class="font-mono">[handoff]</code> comment is posted automatically.
    </p>

    <div class="flex justify-end gap-2">
      <button
        type="button"
        class="rounded-md border border-slate-300 px-3 py-1 text-sm text-slate-700 hover:bg-slate-100"
        onclick={onClose}
      >
        Cancel
      </button>
      <button
        type="submit"
        class="rounded-md bg-slate-900 px-3 py-1 text-sm font-medium text-white hover:bg-slate-700 disabled:opacity-50"
        disabled={store.saving}
      >
        Transfer
      </button>
    </div>
  </form>
</dialog>

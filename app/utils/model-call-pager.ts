import { shallowReactive } from 'vue'

export type CallFilters = {
  modelId?: string
  role?: 'planner' | 'replyer' | 'learning' | 'vision' | 'embedding'
  status?: 'completed' | 'failed'
}
export type CallPage<T> = {
  items: T[]
  nextCursor: number | null
  retentionDays: number
  maxRecords: number
}

// Commit the cursor and rows together. A failed or superseded request never advances a page.
export function createModelCallPager<T>(
  fetchPage: (
    query: CallFilters & { limit: number; beforeId?: number },
    signal: AbortSignal,
  ) => Promise<CallPage<T>>,
) {
  const state = shallowReactive({
    items: [] as T[],
    nextCursor: null as number | null,
    page: 1,
    busy: false,
    failed: false,
    loaded: false,
    retentionDays: 30,
    maxRecords: 100000,
  })
  let filters: CallFilters = {}
  let cursors: Array<number | undefined> = [undefined]
  let generation = 0
  let requestedPage = 1
  let controller: AbortController | undefined
  let disposed = false
  async function load(target: number) {
    if (disposed) return
    requestedPage = target
    const current = ++generation
    controller?.abort()
    controller = new AbortController()
    state.busy = true
    state.failed = false
    try {
      const result = await fetchPage(
        {
          ...filters,
          limit: 50,
          ...(cursors[target - 1] === undefined ? {} : { beforeId: cursors[target - 1] }),
        },
        AbortSignal.any([controller.signal, AbortSignal.timeout(15000)]),
      )
      if (disposed || current !== generation) return
      state.items = result.items
      state.nextCursor = result.nextCursor
      state.page = target
      state.retentionDays = result.retentionDays
      state.maxRecords = result.maxRecords
      state.loaded = true
    } catch {
      if (!disposed && current === generation) state.failed = true
    } finally {
      if (current === generation) state.busy = false
    }
  }
  function reset(nextFilters: CallFilters = filters) {
    filters = { ...nextFilters }
    cursors = [undefined]
    state.items = []
    state.nextCursor = null
    state.page = 1
    state.loaded = false
    return load(1)
  }
  return {
    state,
    reset,
    retry: () => load(requestedPage),
    next: () => {
      if (state.busy || state.nextCursor === null) return
      cursors[state.page] = state.nextCursor
      return load(state.page + 1)
    },
    previous: () => {
      if (state.busy || state.page <= 1) return
      return load(state.page - 1)
    },
    dispose: () => {
      disposed = true
      ++generation
      controller?.abort()
    },
  }
}

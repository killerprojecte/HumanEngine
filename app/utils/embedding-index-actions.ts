import type { components } from '../types/api.d.ts'

type Status = components['schemas']['EmbeddingIndexStatus']
export type EmbeddingIndexAction = 'start' | 'pause' | 'resume' | 'cancel'

/** Derive maintenance actions from the explicit server state, never from coverage or errors. */
export function embeddingIndexActions(status?: Status): EmbeddingIndexAction[] {
  if (!status) return []
  const actions: EmbeddingIndexAction[] = []
  const job = status.job
  if (job?.state === 'running') actions.push('pause')
  if (
    job &&
    (job.state === 'paused' || job.state === 'failed') &&
    status.selected?.encoderRevision === job.encoderRevision
  )
    actions.push('resume')
  if (status.state === 'needs_rebuild' && status.selected && job?.state !== 'running')
    actions.push('start')
  if (job && ['running', 'paused', 'failed'].includes(job.state)) actions.push('cancel')
  return actions
}

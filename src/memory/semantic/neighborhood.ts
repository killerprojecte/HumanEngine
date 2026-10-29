import type { SemanticEpisodeGroup } from './episode-anchors.ts'
import type { MemoryRecord } from '../domain/contracts.ts'
import type { ModelReadTicket } from '../read/model-read-policy.ts'
import {
  semanticHash,
  type SemanticAssertion,
  type SemanticContext,
  type SemanticRef,
} from './contracts.ts'
import type { SemanticDirty, SemanticInput, SemanticResolvedInput } from './input.ts'
import { semanticInputUnchanged } from './input.ts'

/** Original declarations only. Bounded complete subject partitions never silently omit a conflict. */
export class SemanticNeighborhood {
  constructor(
    private readonly ports: {
      records(ctx: SemanticContext): readonly MemoryRecord[]
      references(ctx: SemanticContext, records: readonly MemoryRecord[]): readonly SemanticRef[]
      current(ctx: SemanticContext, ref: SemanticRef): SemanticAssertion | undefined
      episodes?(ctx: SemanticContext, ref: SemanticRef): readonly SemanticEpisodeGroup[]
    },
  ) {}
  resolve(
    dirty: readonly SemanticDirty[],
    ticket: ModelReadTicket,
  ): SemanticResolvedInput | undefined {
    const sceneId = dirty[0]?.sceneId
    if (!sceneId || dirty.some((d) => d.sceneId !== sceneId) || ticket.sceneId !== sceneId) return
    const ctx = { sceneId, simulation: false }
    // Pending notifications are not captured model inputs. Drop obsolete tickets without
    // upgrading them; a separately received exact-current notification must remain.
    const first = dirty.find((item) => {
      const value = this.ports.current(ctx, item.trigger.ref)
      return !!value && value.dependencyKey === item.trigger.dependencyKey
    })
    if (!first) return
    if (first.episodeAnchor) {
      if (!ticket.personalizationEnabled) return
      return this.episode(ctx, first.trigger.ref, first.episodeAnchor)
    }
    const trigger = this.ports.current(ctx, first.trigger.ref)
    if (!trigger || trigger.record.scope.kind !== 'scene') return
    const personId = trigger.record.scope.personId ?? null
    if (personId && !ticket.personalizationEnabled) return
    const records = this.ports
      .records(ctx)
      .filter(
        (record) =>
          !record.internalUse &&
          record.origin === 'learned' &&
          record.scope.kind === 'scene' &&
          record.scope.sceneId === ctx.sceneId &&
          (record.scope.personId ?? null) === personId &&
          (ticket.personalizationEnabled || ['fact', 'claim', 'term'].includes(record.kind)),
      )
    if (records.length > 12) return { coverage: 'limited', entries: [] }
    const refs = this.ports.references(ctx, records)
    if (refs.length !== records.length) return
    const entries = refs.flatMap((ref) => {
      const assertion = this.ports.current(ctx, ref)
      return assertion
        ? [{ selection: { ref, dependencyKey: assertion.dependencyKey }, assertion }]
        : []
    })
    return {
      coverage: entries.length === records.length ? 'complete' : 'limited',
      entries,
      partitionKey: this.partitionKey(ctx, personId),
    }
  }
  private episode(
    ctx: SemanticContext,
    trigger: SemanticRef,
    anchor: { id: string; version: number },
  ): SemanticResolvedInput | undefined {
    const group = this.ports
      .episodes?.(ctx, trigger)
      .find((g) => g.anchor.id === anchor.id && g.anchor.version === anchor.version)
    if (!group) return
    const entries = group.parents.flatMap((selection) => {
      const assertion = this.ports.current(ctx, selection.ref)
      return assertion && assertion.dependencyKey === selection.dependencyKey
        ? [{ selection, assertion }]
        : []
    })
    if (entries.length !== group.parents.length) return
    return {
      coverage: 'complete',
      entries,
      episodeAnchor: group.anchor,
      episodeMembershipKey: group.anchorMembershipKey,
      anchorDependencies: group.anchorDependencies,
      anchorQualifications: group.anchorQualifications,
    }
  }
  partitionKey(ctx: SemanticContext, personId: string | null): string | undefined {
    const records = this.ports
      .records(ctx)
      .filter(
        (record) =>
          !record.internalUse &&
          record.origin === 'learned' &&
          record.scope.kind === 'scene' &&
          record.scope.sceneId === ctx.sceneId &&
          (record.scope.personId ?? null) === personId,
      )
    if (records.length < 2 || records.length > 12) return
    const refs = this.ports.references(ctx, records)
    const selections = refs.flatMap((ref) => {
      const value = this.ports.current(ctx, ref)
      return value ? [{ ref, dependencyKey: value.dependencyKey }] : []
    })
    if (selections.length !== records.length) return
    return semanticHash(
      selections.sort((a, b) => (a.ref.id < b.ref.id ? -1 : a.ref.id > b.ref.id ? 1 : 0)),
    )
  }
  valid(snapshot: SemanticInput): boolean {
    const ctx = { sceneId: snapshot.sceneId, simulation: false }
    if (snapshot.episodeAnchor) {
      const current = this.episode(ctx, snapshot.selections[0]!.ref, snapshot.episodeAnchor)
      return !!current && semanticInputUnchanged(snapshot, current)
    }
    const entries = snapshot.selections.flatMap((selection) => {
      const assertion = this.ports.current(ctx, selection.ref)
      return assertion
        ? [{ selection: { ref: selection.ref, dependencyKey: assertion.dependencyKey }, assertion }]
        : []
    })
    if (!snapshot.episodeOnly) {
      const expected = semanticHash(
        [...snapshot.selections].sort((a, b) =>
          a.ref.id < b.ref.id ? -1 : a.ref.id > b.ref.id ? 1 : 0,
        ),
      )
      if (this.partitionKey(ctx, snapshot.personId) !== expected) return false
    }
    return semanticInputUnchanged(snapshot, {
      coverage: 'complete',
      entries,
      partitionKey: this.partitionKey(ctx, snapshot.personId),
      ...(snapshot.episodeAnchor ? { episodeAnchor: snapshot.episodeAnchor } : {}),
    })
  }
}

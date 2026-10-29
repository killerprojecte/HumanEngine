import { createHash } from 'node:crypto'
import type { Scene } from '../../schema.ts'
const semanticConfigurationKeys = [
  'memorySemanticEnabled',
  'memorySemanticAutoAdmissionEnabled',
  'memorySemanticAnswerEnabled',
] as const
type SemanticConfigurationKey = (typeof semanticConfigurationKeys)[number]
const knowledgeConfigurationKeys = [
  'memoryKnowledgeEnabled',
  'memoryKnowledgeAutoAdmissionEnabled',
  'memoryKnowledgeAnswerEnabled',
] as const
type KnowledgeConfigurationKey = (typeof knowledgeConfigurationKeys)[number]
const workingSetConfigurationKeys = ['memoryWorkingSetEnabled', 'memoryEpisodesEnabled'] as const
type WorkingSetConfigurationKey = (typeof workingSetConfigurationKeys)[number]
const commitmentConfigurationKeys = [
  'memoryCommitmentsEnabled',
  'memoryCommitmentsAnswerEnabled',
  'memoryHistoricalReadEnabled',
] as const
type CommitmentConfigurationKey = (typeof commitmentConfigurationKeys)[number]
export const configurationKeys = [
  'memoryExperienceFeedbackEnabled',
  ...semanticConfigurationKeys,
  ...knowledgeConfigurationKeys,
  'topicAdmissionEnabled',
  ...workingSetConfigurationKeys,
  ...commitmentConfigurationKeys,
  'sourceEventRevisionsEnabled',
  'cacheEnabled',
  'debugModelIO',
  'memoryProjectionEnabled',
  'memoryGraphEnabled',
  'memoryGraphAutoAdmissionEnabled',
  'memoryGraphAnswerEnabled',
  'memoryGraphShadowEnabled',
  'memoryExperienceEnabled',
  'memoryExperienceAutoActivateEnabled',
  'memoryExperienceHintsEnabled',
] as const
export const sceneFlagKeys = [
  'participationEnabled',
  'learningEnabled',
  'proactiveEnabled',
  'memoryReadEnabled',
  'personalizationEnabled',
] as const
export type ConfigurationFlags = Record<(typeof configurationKeys)[number], boolean>
type LegacyConfigurationFlags = Omit<
  ConfigurationFlags,
  KnowledgeConfigurationKey | SemanticConfigurationKey | 'memoryExperienceFeedbackEnabled'
>
export type SceneFlags = Record<(typeof sceneFlagKeys)[number], boolean>
export type ConfigurationRecoveryEvent =
  | {
      kind: 'configuration_flags'
      schemaVersion: 1
      flags: Omit<
        LegacyConfigurationFlags,
        | 'topicAdmissionEnabled'
        | 'sourceEventRevisionsEnabled'
        | CommitmentConfigurationKey
        | WorkingSetConfigurationKey
      >
    }
  | {
      kind: 'configuration_flags'
      schemaVersion: 2
      flags: Omit<
        LegacyConfigurationFlags,
        'topicAdmissionEnabled' | CommitmentConfigurationKey | WorkingSetConfigurationKey
      >
    }
  | {
      kind: 'configuration_flags'
      schemaVersion: 3
      flags: Omit<LegacyConfigurationFlags, 'topicAdmissionEnabled' | WorkingSetConfigurationKey>
    }
  | {
      kind: 'configuration_flags'
      schemaVersion: 4
      flags: Omit<LegacyConfigurationFlags, 'topicAdmissionEnabled'>
    }
  | { kind: 'configuration_flags'; schemaVersion: 5; flags: LegacyConfigurationFlags }
  | {
      kind: 'configuration_flags'
      schemaVersion: 6
      flags: Omit<ConfigurationFlags, SemanticConfigurationKey | 'memoryExperienceFeedbackEnabled'>
    }
  | {
      kind: 'configuration_flags'
      schemaVersion: 7
      flags: Omit<ConfigurationFlags, 'memoryExperienceFeedbackEnabled'>
    }
  | { kind: 'configuration_flags'; schemaVersion: 8; flags: ConfigurationFlags }
  | {
      kind: 'scene_flags'
      schemaVersion: 1
      sceneId: string
      identityHash: string
      flags: Omit<SceneFlags, 'memoryReadEnabled' | 'personalizationEnabled'>
    }
  | {
      kind: 'scene_flags'
      schemaVersion: 2
      sceneId: string
      identityHash: string
      flags: SceneFlags
    }
export function configurationFlags(value: Record<string, unknown>): ConfigurationFlags {
  return Object.fromEntries(
    configurationKeys.map((key) => [
      key,
      key === 'cacheEnabled' ? value[key] !== false : value[key] === true,
    ]),
  ) as ConfigurationFlags
}
export function sceneFlags(scene: Scene): SceneFlags {
  return Object.fromEntries(
    sceneFlagKeys.map((key) => [
      key,
      key === 'memoryReadEnabled' || key === 'personalizationEnabled'
        ? scene[key] !== false
        : scene[key],
    ]),
  ) as SceneFlags
}
export function sceneIdentityHash(scene: Scene): string {
  return createHash('sha256')
    .update(
      JSON.stringify([
        scene.id,
        scene.adapterId,
        scene.accountId,
        scene.platformSceneId,
        scene.kind,
      ]),
    )
    .digest('hex')
}
export function parseConfigurationRecoveryEvent(value: unknown): ConfigurationRecoveryEvent {
  const e = value as ConfigurationRecoveryEvent
  const keys =
    e?.kind === 'configuration_flags'
      ? configurationKeys.filter(
          (key) =>
            (e.schemaVersion >= 8 || key !== 'memoryExperienceFeedbackEnabled') &&
            (e.schemaVersion >= 7 ||
              !semanticConfigurationKeys.includes(key as SemanticConfigurationKey)) &&
            (e.schemaVersion >= 6 ||
              !knowledgeConfigurationKeys.includes(key as KnowledgeConfigurationKey)) &&
            (e.schemaVersion >= 5 || key !== 'topicAdmissionEnabled') &&
            (e.schemaVersion !== 1 || key !== 'sourceEventRevisionsEnabled') &&
            (e.schemaVersion >= 4 ||
              !workingSetConfigurationKeys.includes(key as WorkingSetConfigurationKey)) &&
            (e.schemaVersion >= 3 ||
              !commitmentConfigurationKeys.includes(key as CommitmentConfigurationKey)),
        )
      : sceneFlagKeys.filter(
          (key) =>
            e?.schemaVersion !== 1 ||
            !['memoryReadEnabled', 'personalizationEnabled'].includes(key),
        )
  if (
    !e ||
    !['configuration_flags', 'scene_flags'].includes(e.kind) ||
    (e.kind === 'configuration_flags'
      ? ![1, 2, 3, 4, 5, 6, 7, 8].includes(e.schemaVersion)
      : ![1, 2].includes(e.schemaVersion)) ||
    !e.flags ||
    Object.keys(e.flags).length !== keys.length ||
    !keys.every((key) => typeof (e.flags as Record<string, unknown>)[key] === 'boolean')
  )
    throw new Error('recovery_configuration_invalid')
  if (
    e.kind === 'scene_flags' &&
    (typeof e.sceneId !== 'string' ||
      !e.sceneId ||
      e.sceneId.length > 2000 ||
      !/^[a-f0-9]{64}$/.test(e.identityHash) ||
      Object.keys(e).length !== 5)
  )
    throw new Error('recovery_configuration_invalid')
  if (e.kind === 'configuration_flags' && Object.keys(e).length !== 3)
    throw new Error('recovery_configuration_invalid')
  return e
}

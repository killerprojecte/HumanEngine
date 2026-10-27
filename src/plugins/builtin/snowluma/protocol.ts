import type { sanitizeError } from '../../../diagnostics/errors.ts'
import { safeNetworkDetails } from '../../errors.ts'

export interface SceneRef {
  id: string
  kind: 'group' | 'private'
  adapterId: string
  accountId: string
  platformSceneId: string
}
export type MessagePart =
  | { type: 'text'; text: string }
  | { type: 'mention'; userId: string }
  | { type: 'media'; assetId: string; mediaType: string }
  | { type: 'unsupported'; description: string }
export interface Message {
  id: string
  sceneId: string
  senderId: string
  sentAt: string
  receivedAt: string
  origin: 'live' | 'history'
  parts: MessagePart[]
  replyToId?: string
  status: 'available' | 'recalled' | 'unavailable'
}
export interface RecallEvent {
  sceneId: string
  messageId: string
  occurredAt: string
}
export interface SendSegment {
  parts: MessagePart[]
  replyToId?: string
}
export interface SendResult {
  status: 'sent' | 'failed' | 'unknown' | 'cancelled'
  messageId?: string
  detail?: string
}
export interface HistoryResult {
  items: Message[]
  coverage: 'complete' | 'partial' | 'unavailable'
  nextCursor?: string
  reason?: string
}
export interface OneBotProfile {
  id: string
  groupHistoryAction?: string
  privateHistoryAction?: string
  capabilities: {
    singleMessage: boolean
    groupHistory: boolean
    privateHistory: boolean
    around: boolean
    byUser: boolean
    animation: 'unverified' | 'unsupported'
  }
}
export const ONEBOT_V11_PROFILE: OneBotProfile = {
  id: 'onebot-v11',
  capabilities: {
    singleMessage: true,
    groupHistory: false,
    privateHistory: false,
    around: false,
    byUser: false,
    animation: 'unverified',
  },
}
export const SNOWLUMA_PROFILE: OneBotProfile = {
  id: 'snowluma',
  groupHistoryAction: 'get_group_msg_history',
  privateHistoryAction: 'get_friend_msg_history',
  capabilities: {
    singleMessage: true,
    groupHistory: true,
    privateHistory: true,
    around: false,
    byUser: false,
    animation: 'unverified',
  },
}
export interface OneBotConfig {
  id: string
  accountId: string
  url: string
  credentialEnv?: string
  token?: string
  eventToken?: string
  transport?: 'websocket' | 'http'
  eventHost?: '127.0.0.1' | '::1' | '0.0.0.0' | '::'
  eventPort?: number
  eventPath?: string
  eventCredentialEnv?: string
  timeoutMs?: number
  reconnectMs?: number
}
export interface OneBotCallbacks {
  onMessage?: (message: Message, scene: SceneRef) => void
  onRecall?: (event: RecallEvent, scene: SceneRef) => void
  onStatus?: (status: 'connecting' | 'connected' | 'disconnected' | 'disposed') => void
  onError?: (code: string, details?: OneBotErrorDetails) => void
  isRecalled?: (sceneId: string, messageId: string) => boolean
  resolveMedia?: (
    assetId: string,
    scene: SceneRef,
  ) => { file: string; type: 'image' | 'record' | 'video' } | undefined
}
export type Obj = Record<string, unknown>
export const object = (value: unknown): Obj => {
  if (!value || typeof value !== 'object' || Array.isArray(value))
    throw new OneBotError('invalid_payload')
  return value as Obj
}
export const rawId = (value: unknown): string => {
  if (typeof value === 'number' && Number.isSafeInteger(value)) return String(value)
  if (typeof value === 'string' && value.length > 0 && value.length <= 256) return value
  throw new OneBotError('invalid_identifier')
}
export const encode = (value: unknown) => Buffer.from(JSON.stringify(value)).toString('base64url')
export const decode = (value: string): unknown => {
  try {
    return JSON.parse(Buffer.from(value, 'base64url').toString('utf8'))
  } catch {
    throw new OneBotError('invalid_identifier')
  }
}
export const time = (value: unknown): string => {
  if (typeof value !== 'number' || !Number.isFinite(value) || Math.abs(value) > 8_640_000_000_000)
    throw new OneBotError('invalid_timestamp')
  return new Date(value * 1000).toISOString()
}
export interface OneBotErrorDetails extends Partial<ReturnType<typeof sanitizeError>> {
  systemCode?: string
  httpStatus?: number
}
export class OneBotError extends Error {
  readonly details: OneBotErrorDetails
  constructor(
    public readonly code: string,
    public readonly transmitted = false,
    details: OneBotErrorDetails = {},
  ) {
    super(code)
    this.details = safeNetworkDetails({ code: details.systemCode, httpStatus: details.httpStatus })
    this.name = 'OneBotError'
  }
}
export interface Pending {
  resolve: (value: unknown) => void
  reject: (error: OneBotError) => void
  cleanup: () => void
  transmitted: boolean
}

export function rawIdOrUrl(value: unknown): string {
  if (typeof value !== 'string' || !value || value.length > 16_384)
    throw new OneBotError('invalid_media_reference')
  return value
}

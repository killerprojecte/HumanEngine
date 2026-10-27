import { setTimeout as sleep } from 'node:timers/promises'
import type { SendSegment } from '../../../onebot.ts'
import { TypingDelayConfigSchema, type TypingDelayConfig } from './config.ts'
export const MAX_TYPING_DELAY_MS = 30_000
const graphemes = new Intl.Segmenter(undefined, { granularity: 'grapheme' })

/** Delay one prepared message, not sentences; no message content is retained. */
export function typingDelayMs(segment: SendSegment, config: TypingDelayConfig = {}): number {
  const { speedFactor } = TypingDelayConfigSchema.parse(config)
  const text = segment.parts
    .filter((part) => part.type === 'text')
    .map((part) => part.text)
    .join('')
  const length = [...graphemes.segment(text)].length
  // Five graphemes/second plus a short preparation pause, bounded even for long messages.
  return Math.min(MAX_TYPING_DELAY_MS, Math.round((300 + length * 200) / speedFactor))
}

export function createTypingDelay(config: TypingDelayConfig, signal: AbortSignal) {
  const parsed = TypingDelayConfigSchema.parse(config)
  const controller = new AbortController()
  const combined = AbortSignal.any([signal, controller.signal])
  return {
    signal: combined,
    dispose: () => controller.abort('typing_delay_disabled'),
    async wait(segment: SendSegment, turnSignal: AbortSignal) {
      const active = AbortSignal.any([combined, turnSignal])
      active.throwIfAborted()
      await sleep(typingDelayMs(segment, parsed), undefined, { signal: active })
      active.throwIfAborted()
    },
  }
}

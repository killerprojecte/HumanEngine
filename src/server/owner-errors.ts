import { sanitizeError } from '../diagnostics/errors.ts'

/** Nitro captures these events on the HTTP host. Worker isolates do not inherit
 * its listeners, so retain capture-and-report behavior without logging raw errors. */
export function installOwnerErrorCapture(): void {
  const report = (kind: 'uncaughtException' | 'unhandledRejection', error: unknown) => {
    try {
      console.error('[HumanEngine] Owner background error:', {
        kind,
        ...sanitizeError(error, { operation: 'runtime.background', stage: kind }),
      })
    } catch {
      // Reporting must not turn a captured background error into an owner crash.
      try {
        console.error('[HumanEngine] Owner background error; diagnostics unavailable')
      } catch {
        /* Preserve Nitro's nonfatal capture behavior even if stderr is unavailable. */
      }
    }
  }
  process.on('uncaughtException', (error) => report('uncaughtException', error))
  process.on('unhandledRejection', (error) => report('unhandledRejection', error))
}

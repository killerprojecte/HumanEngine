import { createHash } from 'node:crypto'

/** Count actual recovery row SHA work, independent of public-method dispatch. */
export function recoveryHashProbe() {
  const prototype = Object.getPrototypeOf(createHash('sha256'))
  const update = prototype.update
  let count = 0
  return {
    get count() {
      return count
    },
    start() {
      count = 0
      prototype.update = function (value: unknown, ...args: unknown[]) {
        if (typeof value === 'string' && /\|\d+\|[a-f0-9]{64}\|/.test(value)) count++
        return update.call(this, value, ...args)
      }
    },
    stop() {
      prototype.update = update
    },
  }
}

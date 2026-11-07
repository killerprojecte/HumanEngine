import type { Head } from '../memory/recovery/ledger.ts'
export interface HttpResult {
  status: number
  headers: [string, string][]
  body: Uint8Array
  observedHead?: Head
}

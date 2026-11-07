import Database from 'better-sqlite3'
import { ServerResponse } from 'node:http'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import {
  authorityPath,
  metadata,
  sameHead,
  RECOVERY_READER_CAPABILITY,
} from '../memory/recovery/ledger.ts'
import type {
  ConfigReadPath,
  PublicationInfo,
  PublicationTicket,
  PublicationVerifiedResult,
} from './config-read.ts'
export type { PublicationInfo } from './config-read.ts'
import type { HttpResult } from './http-result.ts'
import { WorkerRpc } from './worker-rpc.ts'

export type PublicationPlan = { result: HttpResult; path?: ConfigReadPath }

// SQLite's existing default busy-handler sleep schedule. Only actual lock contention
// consumes its original allowance; queueing, worker CPU, and IPC are not new deadlines.
const busyDelays = [1, 2, 5, 10, 15, 20, 25, 25, 25, 50, 50, 100]
function busy(error: unknown): boolean {
  return !!error && typeof error === 'object' && 'code' in error && error.code === 'SQLITE_BUSY'
}
function abort(signal: AbortSignal) {
  if (signal.aborted) throw new Error('request_aborted')
}
function normalize(result: HttpResult, target: ServerResponse) {
  const status = result.status,
    body = result.body,
    headers = new Headers(result.headers)
  if (
    !(target instanceof ServerResponse) ||
    target.destroyed ||
    target.writableEnded ||
    !Number.isInteger(status) ||
    status < 200 ||
    status > 599 ||
    !(body instanceof Uint8Array && body.buffer instanceof ArrayBuffer)
  )
    throw new Error('configuration_read_result_invalid')
  for (const name of [
    'connection',
    'keep-alive',
    'proxy-authenticate',
    'proxy-authorization',
    'te',
    'trailer',
    'transfer-encoding',
    'upgrade',
  ])
    headers.delete(name)
  return { status, body, headers: Object.fromEntries(headers) }
}

/** The HTTP host owns the final authority lock, including after a verifier crash.
 * These private connections do no migrations, chain scans, application work, or
 * native busy-handler sleeps. All expensive verification runs on the readonly worker. */
export class PublicationGate {
  private readonly main: Database.Database
  private readonly authority: Database.Database
  private readonly stop = new AbortController()
  private tail: Promise<void> = Promise.resolve()
  private closing?: Promise<void>
  constructor(
    private readonly database: string,
    private readonly info: PublicationInfo,
    private readonly reader: WorkerRpc,
  ) {
    if (
      !Number.isSafeInteger(info.mainTimeout) ||
      info.mainTimeout < 0 ||
      !Number.isSafeInteger(info.authorityTimeout) ||
      info.authorityTimeout < 0 ||
      authorityPath(database, info.authorityPath) !== info.authorityPath
    )
      throw new Error('publication_connection_invalid')
    this.main = new Database(database, { readonly: true, fileMustExist: true, timeout: 0 })
    try {
      this.main.pragma('query_only = ON')
      this.authority = new Database(join(info.authorityPath, 'authority.sqlite'), {
        readonly: true,
        fileMustExist: true,
        timeout: 0,
      })
      try {
        this.authority.pragma('query_only = ON')
      } catch (error) {
        this.authority.close()
        throw error
      }
    } catch (error) {
      this.main.close()
      throw error
    }
  }
  publish(plan: PublicationPlan, target: ServerResponse, signal: AbortSignal): Promise<void> {
    if (this.closing) return Promise.reject(new Error('publication_closed'))
    const combined = AbortSignal.any([signal, this.stop.signal])
    if (combined.aborted) return Promise.reject(new Error('request_aborted'))
    // Serialize only this fixed protocol, not request admission. Canceled queued
    // slots retain no body or transport and cannot overtake the active handshake.
    let queued: { plan: PublicationPlan; target: ServerResponse } | undefined = { plan, target }
    const job = this.tail.then(() => {
      abort(combined)
      const current = queued!
      queued = undefined
      return this.publishOne(current.plan, current.target, combined)
    })
    this.tail = job.catch(() => {})
    return new Promise<void>((resolve, reject) => {
      const canceled = () => {
        queued = undefined
        reject(new Error('request_aborted'))
      }
      combined.addEventListener('abort', canceled, { once: true })
      job.then(
        () => {
          combined.removeEventListener('abort', canceled)
          resolve()
        },
        (error) => {
          combined.removeEventListener('abort', canceled)
          reject(error)
        },
      )
    })
  }

  private unlock() {
    try {
      try {
        if (this.authority.open && this.authority.inTransaction) this.authority.exec('ROLLBACK')
      } finally {
        if (this.main.open && this.main.inTransaction) this.main.exec('ROLLBACK')
      }
    } catch (error) {
      // A failed rollback leaves snapshot ownership uncertain. Do not reuse it or
      // keep accepting publications; close only these read-only host connections.
      this.stop.abort()
      try {
        if (this.authority.open) this.authority.close()
      } finally {
        if (this.main.open) this.main.close()
      }
      throw error
    }
  }

  private async publishOne(plan: PublicationPlan, target: ServerResponse, signal: AbortSignal) {
    const normalized = normalize(plan.result, target)
    const spent = { main: 0, authority: 0 },
      attempts = { main: 0, authority: 0 }
    for (;;) {
      abort(signal)
      let grant: PublicationTicket | undefined
      let verifiedComplete = false
      let acquiring = false
      let contention: 'main' | 'authority' | undefined
      let stage: 'main' | 'authority' = 'main'
      try {
        // Actor readiness means both snapshots are already pinned, not merely BEGIN.
        grant = await this.reader.call<PublicationTicket>('publication-begin', null, {
          signal,
          lease: true,
        })
        abort(signal)
        acquiring = true
        this.main.exec('BEGIN')
        const gate = metadata(this.main)
        stage = 'authority'
        this.authority.exec('BEGIN')
        this.authority.prepare('SELECT rootpage FROM main.sqlite_master LIMIT 1').get()
        acquiring = false
        if (this.authority.pragma('main.journal_mode', { simple: true }) !== 'delete')
          throw new Error('recovery_read_journal_unsupported')
        if (
          this.main.pragma('main.journal_mode', { simple: true }) !== 'wal' ||
          !gate ||
          gate.state !== 'ready' ||
          gate.minimumReader > RECOVERY_READER_CAPABILITY ||
          gate.authority !== this.info.authorityPath
        )
          throw new Error('recovery_gate_invalid')
        // No fresh lock acquisition is permitted in this worker operation. A waiting
        // writer therefore cannot deadlock verification behind the host's read lock.
        const verified = await this.reader.call<PublicationVerifiedResult>(
          'publication-verify',
          {
            ticket: grant.ticket,
            ...(plan.path ? { path: plan.path } : {}),
          },
          { signal },
        )
        verifiedComplete = true
        abort(signal)
        if (
          !sameHead(gate.head, verified.observedHead) ||
          (!plan.path &&
            (!plan.result.observedHead || !sameHead(gate.head, plan.result.observedHead)))
        )
          throw new Error('configuration_read_result_stale')
        const body = plan.path ? verified.body : normalized.body
        if (
          !(body instanceof Uint8Array && body.buffer instanceof ArrayBuffer) ||
          target.destroyed ||
          target.writableEnded
        )
          throw new Error('configuration_read_result_invalid')
        // No await between final check and enqueueing bytes. Worker termination cannot
        // release this connection's lock, and an authority withdrawal cannot commit yet.
        target.writeHead(normalized.status, normalized.headers)
        target.end(body)
        return
      } catch (error) {
        if (!acquiring || !busy(error)) throw error
        contention = stage
      } finally {
        try {
          this.unlock()
        } finally {
          if (grant) {
            try {
              // Release the actor BEFORE any retry or sleep. A PENDING writer may be
              // waiting on that actor while denying the host a new SHARED lock.
              if (!verifiedComplete)
                await this.reader.call('publication-abandon', { ticket: grant.ticket })
            } finally {
              this.reader.releaseResult(grant)
            }
          }
        }
      }

      abort(signal)
      const timeout = contention === 'main' ? this.info.mainTimeout : this.info.authorityTimeout
      const key = contention!
      const remaining = timeout - spent[key]
      if (remaining <= 0) throw new Error('publication_database_busy')
      const sleep = Math.min(
        remaining,
        busyDelays[Math.min(attempts[key]++, busyDelays.length - 1)]!,
      )
      spent[key] += sleep
      await delay(sleep, undefined, { signal })
    }
  }
  close(): Promise<void> {
    if (this.closing) return this.closing
    this.stop.abort()
    return (this.closing = this.tail.then(() => {
      try {
        this.unlock()
        if (this.authority.open) this.authority.close()
      } finally {
        if (this.main.open) this.main.close()
      }
    }))
  }
}

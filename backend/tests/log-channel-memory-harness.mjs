/** Test boundary only. Execute the real channel AND immutable worker algorithms.
 * No worker thread, OS fd, subprocess, network, or persistent log file is created.
 * This is not evidence for native worker scheduling or physical OS interruption.
 */
import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import path from 'node:path'
import vm from 'node:vm'
import ts from 'typescript'

export const sourceURL = new URL('../../src/diagnostics/log-writer.ts', import.meta.url)
export const sourceText = readFileSync(sourceURL, 'utf8')
export const sourceHash = createHash('sha256').update(sourceText).digest('hex')
const plain = value => JSON.parse(JSON.stringify(value))

export function memoryChannels() {
  const workers = [], files = new Map(), descriptors = new Map(), aliases = new Map()
  const directories = new Set(['/'])
  const plans = new Map(), operations = [], backoffs = []
  let nextFD = 10, allocationFailure = false, syncPump = true
  const enoent = () => Object.assign(new Error('synthetic ENOENT'), { code: 'ENOENT' })
  const fail = code => Object.assign(new Error('synthetic sink failure'), { code })
  const target = fd => fd === 1 ? '<stdout>' : descriptors.get(fd)
  const fs = {
    realpathSync(p) {
      if (aliases.has(p)) return aliases.get(p)
      if (directories.has(p) || files.has(p)) return p
      throw enoent()
    },
    mkdirSync(p) { directories.add(p); operations.push(['mkdir', p]) },
    openSync(p, flags, mode) {
      operations.push(['open', p, flags, mode])
      if (fixture.openFailure === p) throw fail('EACCES')
      if (!files.has(p)) files.set(p, Buffer.alloc(0))
      const fd = nextFD++; descriptors.set(fd, p); return fd
    },
    fstatSync(fd) { return { size: files.get(target(fd)).length } },
    readSync(fd, into, offset, length, position) {
      return files.get(target(fd)).copy(into, offset, position, position + length)
    },
    writeSync(fd, bytes, offset, length) {
      const key = target(fd), plan = plans.get(key), step = plan?.shift()
      operations.push(['write', key, offset, length])
      if (step?.code) throw fail(step.code)
      const n = Math.min(length, step?.n ?? length)
      const data = Buffer.from(bytes).subarray(offset, offset + n)
      files.set(key, Buffer.concat([files.get(key) ?? Buffer.alloc(0), data]))
      return n
    },
    fsyncSync(fd) {
      const key = target(fd)
      operations.push(['fsync', key])
      if (fixture.fsyncFailure === key) throw fail('EIO')
    },
    closeSync(fd) { operations.push(['close', target(fd)]); descriptors.delete(fd) },
    unlinkSync(p) { if (!files.delete(p)) throw enoent(); operations.push(['unlink', p]) },
    renameSync(a, b) {
      if (!files.has(a)) throw enoent()
      files.set(b, files.get(a)); files.delete(a); operations.push(['rename', a, b])
    },
  }
  const workerAtomics = Object.create(Atomics)
  workerAtomics.wait = (state, index, expected, timeout) => {
    backoffs.push(timeout)
    fixture.onBackoff?.(state, index, expected, timeout)
    return 'timed-out'
  }
  const mainAtomics = Object.create(Atomics)
  mainAtomics.wait = () => {
    if (!syncPump) throw new Error('unexpected_sync_wait')
    assert(fixture.pumpOne(), 'synchronous barrier has no queued control')
    return 'ok'
  }
  const buffer = Object.create(Buffer)
  buffer.allocUnsafeSlow = size => {
    if (allocationFailure) throw new Error('synthetic_allocation_failure')
    return Buffer.allocUnsafeSlow(size)
  }
  class MemoryWorker extends EventEmitter {
    constructor(program, options) {
      super()
      this.messages = []; this.terminated = false; this.exited = false
      this.portClosed = false; this.failKind = undefined
      this.directory = options.workerData.directory
      const parentPort = {
        on: (kind, handler) => { assert.equal(kind, 'message'); this.deliver = handler },
        close: () => { this.portClosed = true },
      }
      // Replace only the three import boundaries. All fs/write/rotation/counter
      // logic comes verbatim from workerSource, not a second implementation.
      const imports = {
        'node:worker_threads': { parentPort, workerData: options.workerData },
        'node:fs': fs,
        'node:path': path,
      }
      let count = 0
      const executable = program.replace(/await import\('(node:worker_threads|node:fs|node:path)'\)/g,
        (_, name) => { count++; return `__imports[${JSON.stringify(name)}]` })
      assert.equal(count, 3, 'source import seam changed')
      this.ready = vm.runInNewContext(executable, {
        __imports: imports, Buffer, Int32Array, BigInt64Array, Atomics: workerAtomics,
      })
      workers.push(this)
    }
    unref() {}
    postMessage(message, transfer = []) {
      if (this.terminated || this.failKind === message.kind) throw new Error('synthetic_post_failure')
      this.messages.push(structuredClone(message, { transfer }))
    }
    terminate() { this.terminated = true; return Promise.resolve(1) }
    confirmExit(code = 0) {
      assert(!this.exited, 'duplicate synthetic exit')
      this.exited = true; this.emit('exit', code)
    }
  }
  const require = name => {
    if (name === 'node:worker_threads') return { Worker: MemoryWorker }
    if (name === 'node:fs') return fs
    if (name === 'node:path') return path
    throw new Error(`unexpected source import: ${name}`)
  }
  const compiled = ts.transpileModule(sourceText, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
    fileName: 'log-writer.ts', reportDiagnostics: true,
  })
  assert.equal(compiled.diagnostics?.length ?? 0, 0)
  const exports = {}
  const install = vm.runInNewContext(`(function(require,exports){${compiled.outputText}\n})`, {
    Buffer: buffer, Int32Array, BigInt64Array, SharedArrayBuffer, Atomics: mainAtomics,
    queueMicrotask, setTimeout, clearTimeout, Date,
  })
  install(require, exports)
  const fixture = {
    ...exports, workers, files, aliases, directories, plans, operations, backoffs,
    fsyncFailure: undefined, openFailure: undefined, onBackoff: undefined,
    set allocationFailure(value) { allocationFailure = value },
    set syncPump(value) { syncPump = value },
    pumpOne(select = () => true) {
      const worker = workers.find(w => !w.terminated && w.messages.length && select(w))
      if (!worker) return false
      assert(worker.deliver, 'worker program did not install handler')
      worker.deliver(worker.messages.shift()); return true
    },
    async settle(promise, select = () => true) {
      let settled = false, value, error
      promise.then(v => { value = v; settled = true }, e => { error = e; settled = true })
      for (let i = 0; i < 100 && !settled; i++) {
        await Promise.all(workers.map(w => w.ready))
        this.pumpOne(select)
        await new Promise(resolve => setImmediate(resolve))
      }
      assert(settled, 'source operation did not settle under controlled sink schedule')
      if (error) throw error
      return plain(value)
    },
    bytes(key = '<stdout>') { return files.get(key) ?? Buffer.alloc(0) },
    status(channel) { return plain(channel.status) },
    finishExits() {
      for (const worker of workers) {
        assert(worker.terminated || worker.portClosed, 'test left a live synthetic owner')
        if (!worker.exited) worker.confirmExit(worker.terminated ? 1 : 0)
      }
    },
  }
  return fixture
}

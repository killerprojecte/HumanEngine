// Synthetic adapter failures shared by the actual TypeScript oracle and workers.
export const modes = [
  'network',
  'timeout',
  'configuration',
  'http401',
  'http429',
  'http503',
  'subclass',
  'clamped-delay',
  'mutated-kind',
  'mutated-status-zero',
  'mutated-status-string',
  'mutated-status-600',
  'mutated-delay-string',
  'mutated-delay-negative',
  'mutated-delay-infinity',
  'mutated-delay-nan',
  'renamed',
  'message-mutates-kind',
  'kind-getter-order',
  'status-getter',
  'ordered-getters',
  'retry-delay-getter',
  'throwing-message',
  'throwing-status',
  'throwing-delay',
  'symbol-status',
  'admission',
  'foreign',
  'shape',
  'named',
  'wrapped',
]
export function makeFailure(ModelProtocolError, mode, reads = []) {
  let error = new ModelProtocolError('network')
  switch (mode) {
    case 'network':
      return error
    case 'timeout':
      return new ModelProtocolError('timeout')
    case 'configuration':
      return new ModelProtocolError('configuration')
    case 'http401':
      return new ModelProtocolError('http', { status: 401 })
    case 'http429':
      return new ModelProtocolError('http', { status: 429, retryAfterMs: 801 })
    case 'http503':
      return new ModelProtocolError('http', { status: 503, retryAfterMs: 7 })
    case 'subclass':
      return new (class extends ModelProtocolError {})('http', { status: 503 })
    case 'clamped-delay':
      return new ModelProtocolError('http', { status: 503, retryAfterMs: 100001 })
    case 'mutated-kind':
      error.kind = 'synthetic-invalid'
      break
    case 'mutated-status-zero':
      error.status = 0
      break
    case 'mutated-status-string':
      error.status = '503'
      break
    case 'mutated-status-600':
      error.status = 600
      break
    case 'mutated-delay-string':
      error.retryAfterMs = '800'
      break
    case 'mutated-delay-negative':
      error.retryAfterMs = -70
      break
    case 'mutated-delay-infinity':
      error.retryAfterMs = Infinity
      break
    case 'mutated-delay-nan':
      error.retryAfterMs = NaN
      break
    case 'renamed':
      error.name = 'SyntheticOtherError'
      error.message = 'synthetic-secret'
      break
    case 'message-mutates-kind':
      Object.defineProperty(error, 'message', {
        get() {
          reads.push('message')
          error.kind = 'timeout'
          return 'synthetic-secret'
        },
      })
      break
    case 'kind-getter-order': {
      let i = 0
      Object.defineProperty(error, 'kind', {
        get() {
          reads.push('kind:' + i)
          return ['network', 'network', 'network', 'timeout'][i++]
        },
      })
      break
    }
    case 'status-getter':
      Object.defineProperty(error, 'status', {
        get() {
          reads.push('status')
          return 503
        },
      })
      break
    case 'ordered-getters':
      for (const [field, value] of Object.entries({
        message: 'synthetic-secret',
        status: undefined,
        kind: 'network',
        retryAfterMs: 801,
      }))
        Object.defineProperty(error, field, {
          get() {
            reads.push(field)
            return value
          },
        })
      break
    case 'retry-delay-getter':
      Object.defineProperty(error, 'retryAfterMs', {
        get() {
          reads.push('retryAfterMs')
          return 801
        },
      })
      break
    case 'throwing-message':
      Object.defineProperty(error, 'message', {
        get() {
          reads.push('message')
          throw new ModelProtocolError('network')
        },
      })
      break
    case 'throwing-status':
      Object.defineProperty(error, 'status', {
        get() {
          reads.push('status')
          throw new ModelProtocolError('network')
        },
      })
      break
    case 'throwing-delay':
      Object.defineProperty(error, 'retryAfterMs', {
        get() {
          reads.push('retryAfterMs')
          throw new ModelProtocolError('network')
        },
      })
      break
    case 'symbol-status':
      error.status = Symbol('synthetic-secret')
      break
    case 'admission':
      error.message = 'memory_read_disabled'
      break
    case 'foreign': {
      const Foreign = class ModelProtocolError extends Error {
        constructor() {
          super('model_protocol_failure')
          this.kind = 'http'
          this.status = 503
        }
      }
      return new Foreign()
    }
    case 'shape':
      return {
        name: 'ModelProtocolError',
        kind: 'http',
        status: 503,
        retryAfterMs: 801,
        retryable: true,
        protocolFailure: { code: 'provider_http_503', status: 503, retryable: true },
      }
    case 'named':
      return Object.assign(Error('model_protocol_failure'), {
        name: 'ModelProtocolError',
        kind: 'http',
        status: 503,
        retryable: true,
      })
    case 'wrapped':
      return Error('synthetic-secret', { cause: error })
    default:
      throw Error('unknown fixture')
  }
  return error
}

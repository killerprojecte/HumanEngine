/** Direct source read-authority oracle. Synthetic graphs only. */
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { AssertionAuthority } from '../../src/memory/read/assertion-authority.ts'
import { parentBindingState } from '../../src/memory/domain/proofs.ts'
import { evaluateProofParents } from '../../src/memory/read/proof-parents.ts'
import { domainSourceCurrent } from '../../src/memory/read/domain-source-events.ts'
assert.equal(process.version, 'v24.22.1')
assert.equal(Intl.DateTimeFormat().resolvedOptions().timeZone, 'UTC')
const now = Date.parse('2026-10-28T12:00:00.000Z')
Date.now = () => now
const source = (messageId = 'm', extra = {}) => ({
  sceneId: 's',
  messageId,
  senderId: 'p',
  ...extra,
})
const record = (id = 'root', extra = {}) => ({
  id,
  scope: { kind: 'scene', sceneId: 's', personId: 'p' },
  kind: 'claim',
  content: `Synthetic ${id}`,
  sources: [source()],
  importance: 'medium',
  confidence: 0.7,
  learnedAt: '2026-01-01T00:00:00.000Z',
  status: 'active',
  locked: false,
  origin: 'learned',
  lineageStatus: 'verified',
  ...extra,
})
const proof = (parents = [], extra = {}) => ({
  sources: [source()],
  parents,
  parentReferences: parents.map((id) => ({ id, version: 1, deletionEpoch: 0 })),
  ...extra,
})
const revision = (r, proofs) => ({ record: r, supports: proofs })
function graph(extra = {}) {
  const root = record(),
    parent = record('parent')
  return {
    records: { root, parent },
    proofs: { root: [proof(['parent'])], parent: [proof()] },
    states: {
      root: { version: 1, deletionEpoch: 0, deleted: false },
      parent: { version: 1, deletionEpoch: 0, deleted: false },
    },
    revisions: {
      root: { 1: revision(root, [proof(['parent'])]) },
      parent: { 1: revision(parent, [proof()]) },
    },
    scenes: { s: { id: 's', adapterId: 'adapter', accountId: 'account' } },
    registered: {},
    recalled: [],
    ...extra,
  }
}
const operations = [
  ['supported', 'root'],
  ['currentApplicable', 'root'],
  ['historySupports', 'root'],
  ['sanitize', 'root'],
  [
    'historicalAssertion',
    { sceneId: 's', simulation: false },
    { id: 'root', version: 1, deletionEpoch: 0 },
  ],
  [
    'relationAuthority',
    { sceneId: 's', simulation: false },
    { id: 'root', version: 1, deletionEpoch: 0 },
  ],
]
const cases = []
const add = (name, change = () => {}, ops = operations) => {
  const input = graph()
  change(input)
  cases.push({ name, input, ops })
}
add('bound-baseline')
add('parent-version-drift', (g) => {
  g.states.parent.version = 2
})
add('parent-epoch-drift', (g) => {
  g.states.parent.deletionEpoch = 1
})
add('parent-terminal-delete', (g) => {
  g.states.parent.deleted = true
})
add('parent-revision-missing', (g) => {
  g.revisions.parent = {}
})
add('parent-missing', (g) => {
  delete g.records.parent
})
add('legacy-parent-superseded', (g) => {
  delete g.proofs.root[0].parentReferences
  g.records.parent.status = 'superseded'
})
add('bound-parent-superseded', (g) => {
  g.records.parent.status = 'superseded'
})
add('recalled-and-alternative', (g) => {
  g.recalled = ['s/m']
  g.proofs.root.push(proof([], { sources: [source('independent')] }))
})
add('source-edit-current-only', (g) => {
  g.registered['s/m'] = {
    identity: {
      sceneId: 's',
      adapterId: 'adapter',
      accountId: 'account',
      messageId: 'm',
      senderId: 'p',
    },
    ref: { eventId: 'e', revision: 2, deletionEpoch: 0 },
  }
})
for (const field of ['adapterId', 'accountId', 'senderId'])
  add(`source-registered-${field}-mismatch`, (g) => {
    g.registered['s/m'] = {
      identity: {
        sceneId: 's',
        adapterId: 'adapter',
        accountId: 'account',
        messageId: 'm',
        senderId: 'p',
        [field]: 'other',
      },
      ref: { eventId: 'e', revision: 1, deletionEpoch: 0 },
    }
  })
for (const [name, ref] of [
  ['exact', { eventId: 'e', revision: 2, deletionEpoch: 0 }],
  ['stale-version', { eventId: 'e', revision: 1, deletionEpoch: 0 }],
  ['stale-epoch', { eventId: 'e', revision: 2, deletionEpoch: 1 }],
  ['wrong-event', { eventId: 'foreign', revision: 2, deletionEpoch: 0 }],
])
  add(`source-bound-${name}`, (g) => {
    g.proofs.root = [proof([], { sources: [source('m', { eventRef: ref })] })]
    g.registered['s/m'] = {
      identity: {
        sceneId: 's',
        adapterId: 'adapter',
        accountId: 'account',
        messageId: 'm',
        senderId: 'p',
      },
      ref: { eventId: 'e', revision: 2, deletionEpoch: 0 },
    }
  })
add('source-bound-missing', (g) => {
  g.proofs.root = [
    proof([], {
      sources: [source('m', { eventRef: { eventId: 'e', revision: 1, deletionEpoch: 0 } })],
    }),
  ]
})
add(
  'source-tuple-preserves-ref-key-order',
  (g) => {
    g.proofs.root = [
      proof([], {
        sources: [
          source('m', { eventRef: { eventId: 'e', revision: 1, deletionEpoch: 0 } }),
          source('m', { eventRef: { revision: 1, eventId: 'e', deletionEpoch: 0 } }),
        ],
      }),
    ]
    g.revisions.root['1'].supports = g.proofs.root
    g.registered['s/m'] = {
      identity: {
        sceneId: 's',
        adapterId: 'adapter',
        accountId: 'account',
        messageId: 'm',
        senderId: 'p',
      },
      ref: { eventId: 'e', revision: 1, deletionEpoch: 0 },
    }
  },
  operations,
)
add('registered-first-legacy', (g) => {
  g.registered['s/m'] = {
    identity: {
      sceneId: 's',
      adapterId: 'adapter',
      accountId: 'account',
      messageId: 'm',
      senderId: 'p',
    },
    ref: { eventId: 'e', revision: 1, deletionEpoch: 0 },
  }
})
add('registered-scene-missing', (g) => {
  g.scenes = {}
  g.registered['s/m'] = {
    identity: {
      sceneId: 's',
      adapterId: 'adapter',
      accountId: 'account',
      messageId: 'm',
      senderId: 'p',
    },
    ref: { eventId: 'e', revision: 1, deletionEpoch: 0 },
  }
})
for (const [name, scope] of [
  ['other-scene', { kind: 'scene', sceneId: 'other', personId: 'p' }],
  ['other-person', { kind: 'scene', sceneId: 's', personId: 'q' }],
  ['scene-parent', { kind: 'scene', sceneId: 's' }],
  ['public-parent', { kind: 'sharedPublicKnowledge' }],
])
  add(name, (g) => {
    g.records.parent.scope = scope
  })
add('retained-person-mismatch', (g) => {
  g.revisions.parent['1'].record = {
    ...g.records.parent,
    scope: { kind: 'scene', sceneId: 's', personId: 'q' },
  }
})
add('retained-quarantine', (g) => {
  g.revisions.parent['1'].record = { ...g.records.parent, lineageStatus: 'quarantined' }
})
add('retained-invalidated', (g) => {
  g.revisions.parent['1'].record = { ...g.records.parent, status: 'invalidated' }
})
add('retained-source-recalled', (g) => {
  g.revisions.parent['1'].record = { ...g.records.parent, sources: [source('old')] }
  g.recalled = ['s/old']
})
add('retained-recursive-proof-missing', (g) => {
  g.revisions.parent['1'].supports = []
})
add('time-future', (g) => {
  g.records.root.validFrom = '2027-01-01T00:00:00Z'
})
add('time-exclusive-until', (g) => {
  g.records.root.validUntil = '2026-10-28T12:00:00Z'
})
add('legacy-invalid-date-is-nan', (g) => {
  g.records.root.validFrom = 'invalid'
  g.records.root.validUntil = 'invalid'
})
add('unverified-current-source', (g) => {
  g.records.root.lineageStatus = 'unverified'
  g.recalled = ['s/m']
})
add('quarantined-root', (g) => {
  g.records.root.lineageStatus = 'quarantined'
})
add('public-sanitize', (g) => {
  g.records.root.scope = { kind: 'sharedPublicKnowledge' }
  g.records.root.observedAt = 'old'
  g.records.root.supersedesId = 'parent'
})
add('simulation-never-exposes', () => {}, [
  [
    'historicalAssertion',
    { sceneId: 's', simulation: true },
    { id: 'root', version: 1, deletionEpoch: 0 },
  ],
  [
    'relationAuthority',
    { sceneId: 's', simulation: true },
    { id: 'root', version: 1, deletionEpoch: 0 },
  ],
])
add('root-version-stale-history-retained', (g) => {
  g.states.root.version = 2
})
add('root-epoch-revoked', (g) => {
  g.states.root.deletionEpoch = 1
})
add('root-current-subject-changed', (g) => {
  g.records.root = { ...g.records.root, scope: { kind: 'scene', sceneId: 's', personId: 'q' } }
})
add('observations-recomputed', (g) => {
  g.records.root.observedAt = 'stale'
  g.proofs.root = [
    proof([], {
      sourceObservations: [
        { ...source(), receivedAt: '2026-02-30T00:00:00Z' },
        { ...source(), receivedAt: '2026-01-02T00:00:00Z' },
        { ...source(), receivedAt: '2026-01-02T01:00:00+01:00' },
        { ...source(), receivedAt: '2026-01-01T24:00:00Z' },
      ],
    }),
  ]
})
add('lossless-source-identities', (g) => {
  const s = source('\ud800', {
    senderId: '\udfff',
    eventRef: { eventId: 'event', revision: 1, deletionEpoch: 0 },
  })
  g.proofs.root = [
    proof([], {
      sources: [s, { ...s, extra: 'last wins' }],
      subjectWitness: { kind: 'self', personId: '\ud800' },
      sourceObservations: [],
    }),
  ]
  g.records.root.sources = [s]
})
// A failed visit through a cycle is not memoized as false. The independent OR
// support of A must allow B when B is later visited outside A's recursion path.
add('positive-only-or-cycle', (g) => {
  g.records.a = record('a')
  g.records.b = record('b')
  g.proofs.root = [proof(['a', 'b'], { parentReferences: undefined })]
  g.proofs.a = [proof(['b'], { parentReferences: undefined }), proof()]
  g.proofs.b = [proof(['a'], { parentReferences: undefined })]
  g.states.a = { version: 0, deletionEpoch: 0, deleted: false }
  g.states.b = { version: 0, deletionEpoch: 0, deleted: false }
})
add('cycle-then-independent-alternative', (g) => {
  g.proofs.root = [proof(['root'], { parentReferences: undefined }), proof()]
})
add('bound-retained-cycle-or', (g) => {
  g.proofs.parent = [proof()]
  g.revisions.parent['1'].supports = [proof(['parent']), proof()]
})
add(
  'source-budget-explicit-error',
  (g) => {
    g.proofs.root = [
      proof([], { sources: Array.from({ length: 4100 }, (_, i) => source(`bulk${i}`)) }),
    ]
  },
  [
    ['supported', 'root'],
    ['historySupports', 'root'],
  ],
)
add(
  'depth-existing-contract',
  (g) => {
    for (let i = 0; i < 66; i++) {
      const id = i === 0 ? 'root' : `d${i}`,
        next = `d${i + 1}`
      g.records[id] = record(id)
      g.proofs[id] = [proof(i === 65 ? [] : [next], { parentReferences: undefined })]
    }
  },
  [['supported', 'root']],
)
add(
  'ready-error-before-simulation',
  (g) => {
    g.fail = 'ready'
  },
  [
    [
      'historicalAssertion',
      { sceneId: 's', simulation: true },
      { id: 'root', version: 1, deletionEpoch: 0 },
    ],
  ],
)
add('record-error-before-state', (g) => {
  g.fail = 'record:root'
})
add('proof-error-propagates', (g) => {
  g.fail = 'proofs:root'
})
add('recalled-error-no-or-fallback', (g) => {
  g.fail = 'recalled:s/m'
  g.proofs.root.push(proof([], { sources: [source('other')] }))
})
const bindingProofs = [
  proof(),
  { sources: [source()], parents: ['parent'] },
  proof(['parent']),
  proof(['parent'], { parentReferences: null }),
  proof(['parent', 'parent']),
  proof(['parent'], { parentReferences: [{ id: 'other', version: 1, deletionEpoch: 0 }] }),
  proof(['parent'], {
    parentReferences: [{ id: 'parent', version: 9007199254740992, deletionEpoch: 0 }],
  }),
  proof(['parent'], { invalidatedBy: 'privacy' }),
]
add(
  'binding-reasons',
  () => {},
  bindingProofs.flatMap((p) => [
    ['parentBindingState', p],
    ['parentReason', p, 'current'],
    ['parentReason', p, 'history'],
  ]),
)
for (const c of cases) {
  // Snapshot JSON first so absent fixture properties match Go's parsed inputs.
  c.input = JSON.parse(JSON.stringify(c.input))
  c.ops = JSON.parse(JSON.stringify(c.ops))
  c.steps = []
  for (const op of c.ops) {
    const calls = []
    const g = c.input
    const call = (name) => {
      calls.push(name)
      if (g.fail === name) throw Error('synthetic_port_failure')
    }
    const repository = {
      get: (id) => {
        call(`record:${id}`)
        return g.records[id]
      },
      supports: (id) => {
        call(`proofs:${id}`)
        return g.proofs[id] ?? []
      },
    }
    const history = {
      state: (id) => {
        call(`state:${id}`)
        return g.states[id] ?? { version: 0, deletionEpoch: 0, deleted: false }
      },
      revision: (id, version, key) => {
        call(`revision:${id}/${version}/${key}`)
        const r = g.revisions[id]?.[version]
        const scope = r?.record.scope
        return r && (scope.kind === 'scene' ? `scene:${scope.sceneId}` : 'shared:public') === key
          ? r
          : undefined
      },
    }
    const store = {
      assertRecoveryReady: () => call('ready'),
      recalled: (s, m) => {
        call(`recalled:${s}/${m}`)
        return g.recalled.includes(`${s}/${m}`)
      },
      scene: (id) => {
        call(`scene:${id}`)
        return g.scenes[id]
      },
      sourceEvents: {
        registered: (s, m) => {
          call(`registered:${s}/${m}`)
          return g.registered[`${s}/${m}`]
        },
        hasEdits: (i) => {
          call(`edits:${i.sceneId}/${i.messageId}`)
          return (g.registered[`${i.sceneId}/${i.messageId}`]?.ref.revision ?? 0) > 1
        },
        current: (ref, i) => {
          call(`current:${i.sceneId}/${i.messageId}`)
          const r = g.registered[`${i.sceneId}/${i.messageId}`]
          return {
            eligible:
              !!r &&
              r.ref.eventId === ref.eventId &&
              r.ref.revision === ref.revision &&
              r.ref.deletionEpoch === ref.deletionEpoch,
          }
        },
      },
    }
    const authority = new AssertionAuthority(store, repository, { history })
    let value = null,
      error
    try {
      const [method, ...args] = op
      if (method === 'parentBindingState') value = parentBindingState(args[0])
      else if (method === 'parentReason')
        value = evaluateProofParents(args[0], args[1], {
          current: (id) => history.state(id),
          revision: (ref) => !!history.revision(ref.id, ref.version, 'scene:s'),
        })
      else if (method === 'domainSourceCurrent') value = domainSourceCurrent(store, args[0])
      else if (['supported', 'currentApplicable', 'sanitize'].includes(method))
        value = authority[method](g.records[args[0]])
      else value = authority[method](...args)
    } catch (e) {
      error = e.message
    }
    c.steps.push({
      op: JSON.stringify(op),
      result: JSON.stringify(value ?? null),
      ...(error ? { error } : {}),
      calls,
    })
  }
  delete c.ops
}
const files = [
  'src/memory/read/provenance.ts',
  'src/memory/read/applicability.ts',
  'src/memory/read/proof-parents.ts',
  'src/memory/read/bound-parents.ts',
  'src/memory/read/assertion-authority.ts',
  'src/memory/read/domain-source-events.ts',
  'src/memory/domain/proofs.ts',
  'src/memory/domain/record-values.ts',
  'src/memory/write/subject-attribution.ts',
]
const sources = Object.fromEntries(
  files.map((p) => [p, createHash('sha256').update(readFileSync(p)).digest('hex')]),
)
const raw =
  JSON.stringify(
    {
      node: process.version,
      locale: Intl.Collator().resolvedOptions().locale,
      timezone: 'UTC',
      now,
      sources,
      cases,
    },
    null,
    2,
  ) + '\n'
const path = 'backend/internal/assertionauthority/testdata/authority.json'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), raw)
else {
  mkdirSync('backend/internal/assertionauthority/testdata', { recursive: true })
  writeFileSync(path, raw)
}
console.log(
  `${cases.length} authority graphs, ${cases.reduce((n, c) => n + c.steps.length, 0)} outcomes`,
)

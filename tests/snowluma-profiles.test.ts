import assert from 'node:assert/strict'
import test from 'node:test'
import { OneBotProfiles, qqAvatar } from '../src/plugins/builtin/snowluma/profiles.ts'
import { normalizeMessage } from '../src/plugins/builtin/snowluma/normalization.ts'
import { OneBotAdapter, type Message } from '../src/onebot.ts'
import { Store } from '../src/store.ts'
const adapter = new OneBotAdapter({ id: 'snow', accountId: '42', url: 'ws://127.0.0.1:1' })
const scene = adapter.scene('group', '9')
const context = {
  accountId: '42',
  scene: () => ({ ...scene }),
  messageId: (s: typeof scene, id: string) => adapter.messageId(s, id),
  userId: (id: string) => adapter.userId(id),
  isRecalled: () => false,
}
const normalize = (sender: Record<string, unknown> = {}, extra = {}) =>
  normalizeMessage(
    context,
    {
      message_type: 'group',
      group_id: 9,
      user_id: 7,
      message_id: 1,
      time: 1000,
      message: [{ type: 'text', data: { text: 'hello' } }],
      sender,
      ...extra,
    },
    'live',
  )
const flush = () => new Promise<void>((resolve) => setImmediate(resolve))

test('presentation uses group card before nickname, safe platform avatars, and stable canonical IDs', () => {
  const result = normalize(
    { card: ' Group card ', nickname: 'Nickname', avatar: 'https://evil.test/tracker' },
    { group_name: 'Group name' },
  )
  assert.equal(result.message.senderDisplayName, 'Group card')
  assert.equal(result.message.senderPlatformId, '7')
  assert.equal(result.message.senderId, adapter.userId('7'))
  assert.equal(result.scene.displayName, 'Group name')
  assert.equal(result.message.senderAvatarUrl, 'https://q1.qlogo.cn/g?b=qq&nk=7&s=100')
  assert.equal(result.scene.avatarUrl, 'https://p.qlogo.cn/gh/9/9/100')
  assert.equal(
    normalize({ card: ' ', nickname: ' Nickname\u202e ' }).message.senderDisplayName,
    'Nickname',
  )
  assert.equal(qqAvatar('7&tracking=x'), undefined)
})

test('missing profiles are nonblocking, cached, refreshed, account-validated, and bounded on failure', async () => {
  let now = 0,
    calls = 0,
    failures = 0
  const updates: any[] = []
  const profiles = new OneBotProfiles(
    async (action) => {
      calls++
      return action === 'get_group_info'
        ? { group_id: '9', group_name: 'Fetched group' }
        : { user_id: '7', nickname: 'Fetched user' }
    },
    (update) => updates.push(update),
    () => failures++,
    () => now,
  )
  let item = normalize()
  profiles.observe(item.message, item.scene)
  assert.equal(updates.length, 0)
  await flush()
  assert.equal(calls, 2)
  item = normalize()
  profiles.observe(item.message, item.scene)
  assert.equal(item.scene.displayName, 'Fetched group')
  assert.equal(item.message.senderDisplayName, 'Fetched user')
  await flush()
  assert.equal(calls, 2)
  now = 31 * 60_000
  profiles.observe(normalize().message, { ...scene })
  await flush()
  assert.equal(calls, 4)
  profiles.dispose()
  const bad = new OneBotProfiles(
    async () => {
      throw new Error('private response secret')
    },
    () => assert.fail('must not update'),
    () => failures++,
    () => now,
  )
  bad.observe(normalize().message, { ...scene })
  await flush()
  bad.observe(normalize().message, { ...scene })
  await flush()
  assert.equal(failures, 2)
  bad.dispose()
})

test('profile disposal drops late results and limits concurrent discovery', async () => {
  let release!: (value: unknown) => void,
    calls = 0,
    updates = 0
  const promise = new Promise((resolve) => (release = resolve))
  const profiles = new OneBotProfiles(
    async () => {
      calls++
      return promise
    },
    () => updates++,
    () => {},
  )
  for (let i = 0; i < 20; i++)
    profiles.observe(
      { ...normalize().message, senderId: `sender-${i}`, senderPlatformId: String(100 + i) },
      { ...scene, id: `scene-${i}` },
    )
  await flush()
  assert.equal(calls, 4)
  profiles.dispose()
  release({ group_id: '9', group_name: 'Late' })
  await flush()
  assert.equal(updates, 0)
})

test('identity persistence updates scene names and old message presentation without crossing scenes', () => {
  const store = new Store(':memory:')
  try {
    store.saveScene({
      ...scene,
      name: '9',
      participationEnabled: false,
      learningEnabled: false,
      proactiveEnabled: false,
    })
    const item = normalize({ nickname: 'Known user' }, { group_name: 'Known group' })
    store.observeIdentity(item.scene, item.message)
    assert.equal(store.scene(scene.id)?.name, 'Known group')
    const old = {
      ...item.message,
      senderDisplayName: undefined,
      senderPlatformId: undefined,
      senderAvatarUrl: undefined,
    } as Message
    assert.equal(store.presentMessage(old).senderDisplayName, 'Known user')
    assert.equal(
      store.presentMessage({ ...old, sceneId: 'unrelated' }).senderDisplayName,
      undefined,
    )
    store.updateIdentity({ scene: { ...scene, accountId: 'wrong', displayName: 'Wrong' } })
    assert.equal(store.scene(scene.id)?.name, 'Known group')
    store.updateIdentity({ scene: { ...scene, displayName: 'New group' } })
    assert.equal(store.scene(scene.id)?.name, 'New group')
  } finally {
    store.close()
  }
})

test('a late profile lookup cannot overwrite a newer event group card', async () => {
  let release!: (value: unknown) => void
  const pending = new Promise((resolve) => {
    release = resolve
  })
  const updates: unknown[] = []
  const profiles = new OneBotProfiles(
    async () => pending,
    (update) => updates.push(update),
    () => {},
  )
  const first = normalize({}, { group_name: 'Group' })
  profiles.observe(first.message, first.scene)
  await flush()
  const newer = normalize(
    { card: 'New group card', nickname: 'Global nickname' },
    { group_name: 'Group' },
  )
  profiles.observe(newer.message, newer.scene)
  release({ user_id: '7', nickname: 'Old global nickname' })
  await flush()
  const latest = normalize({}, { group_name: 'Group' })
  profiles.observe(latest.message, latest.scene)
  assert.equal(latest.message.senderDisplayName, 'New group card')
  assert.equal(updates.length, 0)
  profiles.dispose()
})

test('numeric identifiers on unrelated adapters never acquire QQ avatars or identities', () => {
  const store = new Store(':memory:')
  try {
    const unrelated = {
      ...scene,
      id: 'telegram-group',
      adapterId: 'telegram',
      name: 'Other group',
      participationEnabled: false,
      learningEnabled: false,
      proactiveEnabled: false,
    }
    store.saveScene(unrelated)
    store.updateIdentity({
      scene: { ...unrelated, displayName: 'Wrong name' },
      sender: { id: '7', platformId: '7', displayName: 'Wrong person' },
    })
    assert.equal(store.scene(unrelated.id)?.name, 'Other group')
    assert.equal(store.scene(unrelated.id)?.avatarUrl, undefined)
    const message = {
      ...normalize().message,
      sceneId: unrelated.id,
      senderId: '7',
      senderAvatarUrl: undefined,
    }
    assert.equal(store.presentMessage(message).senderAvatarUrl, undefined)
    assert.equal(store.presentMessage(message).senderDisplayName, undefined)
  } finally {
    store.close()
  }
})

test('real ingress persists human scene metadata without violating strict scene contract or losing flow spans', async () => {
  const { Engine } = await import('../src/engine.ts')
  const { SceneSchema } = await import('../src/schema.ts')
  const store = new Store(':memory:')
  const engine = new Engine(store)
  try {
    const item = normalize({ card: 'Scene card' }, { group_name: 'Human group title' })
    engine.runtime.ingest(item.message, item.scene, false)
    const saved = SceneSchema.parse(store.scene(item.scene.id))
    assert.equal(saved.name, 'Human group title')
    assert.equal(saved.avatarUrl, qqAvatar('9', true))
    assert.equal('displayName' in saved, false)
    assert.equal(engine.runtime.preview(item.scene.id)[0]?.senderDisplayName, 'Scene card')
    assert.ok(store.logs.some((log) => log.stage === 'routing' && log.status === 'completed'))
    assert.ok(store.logs.some((log) => log.stage === 'ingress' && log.status === 'completed'))
  } finally {
    await engine.dispose()
    store.close()
  }
})

import test from 'node:test'
import assert from 'node:assert/strict'
import { OpenAPIHono } from '@hono/zod-openapi'
import { Store } from '../src/store.ts'
import { IdentityRegistry } from '../src/memory/identities/service.ts'
import { installIdentityRoutes } from '../src/memory/interfaces/identity-routes.ts'

test('identity administration has strict CAS, trusted actor, reversible links and scene-qualified aliases', async () => {
  const store = new Store(':memory:')
  const identities = new IdentityRegistry(store.db, { assertReady() {} })
  const app = new OpenAPIHono()
  app.use('/api/*', async (c, next) => {
    if (c.req.header('Authorization') !== 'Bearer synthetic')
      return c.json({ error: 'unauthorized' }, 401)
    await next()
  })
  installIdentityRoutes(app, { identities, store })
  store.saveScene({
    id: 'scene',
    adapterId: 'fixture',
    accountId: 'account',
    kind: 'group',
    platformSceneId: 'scene',
    name: 'Synthetic',
    learningEnabled: false,
    participationEnabled: false,
    proactiveEnabled: false,
  })
  const a = { adapterId: 'fixture', accountId: 'account', personId: 'a' },
    b = { ...a, personId: 'b' },
    c = { ...a, personId: 'c' }
  const headers = { Authorization: 'Bearer synthetic', 'Content-Type': 'application/json' }
  const post = (value: object) =>
    app.request('/api/memory-identities', { method: 'POST', headers, body: JSON.stringify(value) })
  const get = async () => (await app.request('/api/memory-identities', { headers })).json()
  const command = (action: string, expectedRevision: number, rest: object) => ({
    action,
    expectedRevision,
    operationId: `operation-${expectedRevision}-${action}`,
    confirmed: true,
    ...rest,
  })
  try {
    assert.equal((await app.request('/api/memory-identities')).status, 401)
    assert.equal(
      (await post({ ...command('link', 0, { left: a, right: b }), administratorId: 'spoof' }))
        .status,
      400,
    )
    assert.equal(
      (await post({ ...command('link', 0, { left: a, right: b }), confirmed: false })).status,
      400,
    )
    const link = command('link', 0, { left: a, right: b })
    assert.equal((await post(link)).status, 200)
    assert.deepEqual(await (await post(link)).json(), { revision: 1, duplicate: true })
    assert.equal((await post(command('link', 0, { left: a, right: c }))).status, 409)
    assert.equal((await post(command('link', 1, { left: b, right: c }))).status, 200)
    assert.equal((await post(command('link', 2, { left: a, right: c }))).status, 200)
    assert.equal((await post(command('unlink', 3, { left: a, right: b }))).status, 200)
    assert.equal(
      identities.resolveMembers(
        {
          sceneId: 'scene',
          adapterId: 'fixture',
          accountId: 'account',
          allowedPersonIds: ['a', 'b', 'c'],
        },
        'a',
      ).members.length,
      3,
    )
    assert.equal((await post(command('split', 4, { partition: [a] }))).status, 200)
    assert.equal(
      identities.resolveMembers(
        {
          sceneId: 'scene',
          adapterId: 'fixture',
          accountId: 'account',
          allowedPersonIds: ['a', 'b', 'c'],
        },
        'a',
      ).members.length,
      1,
    )
    const alias = command('alias_put', 5, {
      aliasId: 'alias',
      sceneId: 'scene',
      endpoint: a,
      label: 'Synthetic alias',
      validFrom: null,
      validUntil: null,
    })
    assert.equal((await post({ ...alias, sceneId: 'simulation' })).status, 404)
    assert.equal((await post({ ...alias, endpoint: { ...a, accountId: 'foreign' } })).status, 400)
    assert.equal(
      (
        await post({
          ...alias,
          validFrom: '2026-10-29T00:00:00Z',
          validUntil: '2026-10-28T00:00:00Z',
        })
      ).status,
      409,
    )
    assert.equal((await post(alias)).status, 200)
    assert.deepEqual((await get()).aliases[0].source, {
      kind: 'manual',
      administratorId: 'authenticated-admin',
    })
    assert.equal(
      identities.aliases(
        { sceneId: 'foreign', adapterId: 'fixture', accountId: 'account', allowedPersonIds: ['a'] },
        'a',
      ).length,
      0,
    )
    const remove = command('alias_remove', 6, { aliasId: 'alias', sceneId: 'scene' })
    assert.equal((await post(remove)).status, 200)
    assert.equal((await post(remove)).status, 200)
    assert.equal((await get()).aliases.length, 0)
    assert.equal((await get()).revision, 7)
  } finally {
    store.close()
  }
})

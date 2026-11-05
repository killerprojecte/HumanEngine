import test from 'node:test'
import assert from 'node:assert/strict'
import { compactNumber, usageBars, usageLines, usageValue } from '../app/utils/usage-chart.js'

test('compact display uses K/M/B with sensible unit boundaries', () => {
  for (const [number, display] of [
    [0, '0'],
    [42, '42'],
    [1000, '1K'],
    [12340, '12.34K'],
    [999999, '1M'],
    [1234567, '1.23M'],
    [1234567890, '1.23B'],
  ] as const)
    assert.equal(compactNumber(number), display)
  assert.equal(compactNumber(NaN), '—')
})
test('independent values never sum input with its cache subsets', () => {
  const point = {
    input: 10000,
    output: 2000,
    read: 6000,
    write: 1000,
    reported: 2,
    calls: 2,
    readKnown: 2,
    writeKnown: 2,
  }
  assert.deepEqual(
    usageBars(point).map((s) => [s.key, s.value]),
    [
      ['input', 10000],
      ['read', 6000],
      ['output', 2000],
      ['write', 1000],
    ],
  )
  assert.equal(Math.max(...usageBars(point).map((s) => s.value)), 10000)
  assert.equal(usageValue({ ...point, readKnown: 1 }, 'read'), 6000)
  assert.equal(usageValue({ ...point, reported: 0 }, 'input'), null)
  assert.equal(usageValue({ ...point, read: 0 }, 'read'), 0)
  const lines = usageLines([point, { ...point, readKnown: 0 }, point], 'read', 10000)
  assert.equal(lines.length, 2)
  assert.equal(lines[0]![0]!.y, 80)
  assert.equal(lines[1]![0]!.y, 80)
  assert.deepEqual(usageLines([{ ...point, reported: 0 }], 'input', 10000), [])
})

test('reported subsets remain visible and partial, while unavailable series split lines', () => {
  const partial = {
    input: 1200,
    output: 90,
    read: 500,
    write: 0,
    calls: 4,
    reported: 2,
    readKnown: 1,
    writeKnown: 0,
  }
  assert.equal(usageValue(partial, 'input'), 1200)
  assert.equal(usageValue(partial, 'write'), null)
  assert.ok(usageBars(partial).every((bar) => bar.partial))
  assert.equal(usageLines([partial], 'input', 1200)[0]![0]!.partial, true)
  assert.equal(usageLines([partial, { ...partial, reported: 0 }, partial], 'input', 1200).length, 2)
})

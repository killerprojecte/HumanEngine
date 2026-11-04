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
    known: 0,
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
  assert.equal(usageValue({ ...point, readKnown: 1 }, 'read'), null)
  assert.equal(usageValue({ ...point, known: 1 }, 'input'), null)
  assert.equal(usageValue({ ...point, read: 0 }, 'read'), 0)
  const lines = usageLines([point, { ...point, readKnown: 0 }, point], 'read', 10000)
  assert.equal(lines.length, 2)
  assert.equal(lines[0]![0]!.y, 80)
  assert.equal(lines[1]![0]!.y, 80)
  assert.deepEqual(usageLines([{ ...point, known: 1 }], 'input', 10000), [])
})

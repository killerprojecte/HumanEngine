import assert from 'node:assert/strict'
import test from 'node:test'
import { parseCqMessage } from '../src/plugins/builtin/snowluma/normalization.ts'

// Compatibility oracle only for bounded fixtures, never for hostile long inputs.
function legacy(value: string) {
  const decode = (text: string) =>
    text
      .replaceAll('&#91;', '[')
      .replaceAll('&#93;', ']')
      .replaceAll('&#44;', ',')
      .replaceAll('&amp;', '&')
  const result: Record<string, unknown>[] = []
  let cursor = 0
  for (const match of value.matchAll(/\[CQ:([a-zA-Z0-9_]+)((?:,[^\]]*)?)\]/g)) {
    if (match.index > cursor)
      result.push({ type: 'text', data: { text: decode(value.slice(cursor, match.index)) } })
    const data: Record<string, unknown> = {}
    for (const pair of match[2]!.split(',').slice(1)) {
      const equal = pair.indexOf('=')
      if (equal > 0) data[pair.slice(0, equal)] = decode(pair.slice(equal + 1))
    }
    result.push({ type: match[1], data })
    cursor = match.index + match[0].length
  }
  if (cursor < value.length)
    result.push({ type: 'text', data: { text: decode(value.slice(cursor)) } })
  return result
}

test('CQ delimiters, escaped parameters, duplicate keys and one-pass decoding remain compatible', () => {
  assert.deepEqual(parseCqMessage('before&amp;[CQ:at,qq=42]after'), [
    { type: 'text', data: { text: 'before&' } },
    { type: 'at', data: { qq: '42' } },
    { type: 'text', data: { text: 'after' } },
  ])
  assert.deepEqual(
    parseCqMessage('[CQ:image,file=a&#44;b,url=x=y&amp;z,empty=,invalid,=skip,file=last&amp;#44;]'),
    [{ type: 'image', data: { file: 'last&#44;', url: 'x=y&z', empty: '' } }],
  )
  assert.deepEqual(parseCqMessage('&#91;CQ:at,qq=1&#93;'), [
    { type: 'text', data: { text: '[CQ:at,qq=1]' } },
  ])
})

test('CQ nested and malformed candidates preserve the first valid opener and first literal closer', () => {
  for (const value of [
    '',
    'plain',
    ']',
    '[CQ:x]',
    '[CQ:_0]',
    '[cq:x]',
    '[CQ:]',
    '[CQ:,a=b]',
    '[CQ:x-]',
    '[CQ:漢字]',
    '[CQ:x\n]',
    '[CQ:x,a=\nvalue]',
    '[CQ:x,]',
    '[CQ:x,a=[CQ:y,b=2]]',
    '[CQ:x[CQ:y]]',
    '[CQ:,bad[CQ:z,k=v]]',
    'text]junk[CQ:a][CQ:b,k=v]tail',
    '[CQ:x,[CQ:y,',
    '[CQ:x,unclosed&amp;',
    '[CQ:x,a=1,,b=2,a=3]',
    '[CQ:x,constructor=ok,__proto__=value,toString=text]',
  ])
    assert.deepEqual(parseCqMessage(value), legacy(value), JSON.stringify(value))
})

test('CQ parser matches the legacy grammar across deterministic short mixed inputs', () => {
  const atoms = [
    '[CQ:',
    'x',
    '_0',
    '漢',
    ',',
    '=',
    '[',
    ']',
    '\n',
    '\r',
    ' ',
    '-',
    'a=b',
    'qq=all',
    '&#91;',
    '&#93;',
    '&#44;',
    '&amp;',
    '[CQ:image,url=x]',
    '[CQ:at,qq=42]',
  ]
  let seed = 0x68ac0321
  const random = () => (seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0)
  for (let i = 0; i < 2000; i++) {
    const value = Array.from(
      { length: 1 + (random() % 32) },
      () => atoms[random() % atoms.length]!,
    ).join('')
    assert.deepEqual(parseCqMessage(value), legacy(value), JSON.stringify(value))
  }
})

test('CQ parsing scales without rescanning repeated unterminated prefixes', () => {
  const small = '[CQ:x,'.repeat(2000)
  const large = '[CQ:x,'.repeat(16000)
  const time = (value: string) => {
    const samples: number[] = []
    for (let i = 0; i < 3; i++) {
      const start = performance.now()
      const parsed = parseCqMessage(value)
      samples.push(performance.now() - start)
      assert.deepEqual(parsed, [{ type: 'text', data: { text: value } }])
    }
    return Math.min(...samples)
  }
  time(small)
  const baseline = time(small)
  const elapsed = time(large)
  // Eight times the input must not incur quadratic work. The absolute floor
  // tolerates timer noise and slow shared runners for this sub-millisecond path.
  assert.ok(
    elapsed < Math.max(150, baseline * 20),
    `12KB=${baseline.toFixed(2)}ms; 96KB=${elapsed.toFixed(2)}ms`,
  )
  // Repeated invalid prefixes inside closed windows and many closers must also
  // terminate, while the valid inner code is still recognized.
  const mixed = '][CQ:-'.repeat(16000) + '[CQ:at,qq=42]'
  const start = performance.now()
  assert.deepEqual(parseCqMessage(mixed).at(-1), { type: 'at', data: { qq: '42' } })
  assert.ok(performance.now() - start < 1000)
})

/** SYNTHETIC ONLY: actual SecretStore and Node Buffer/crypto byte oracle.
 * node --import tsx backend/tests/generate-credential-byte-fixtures.mjs [--check]
 * Uses only in-memory SQLite. Never opens a key path or an installation DB.
 * Fixed keys and repeated nonces are solely for reproducible test vectors,
 * never production encryption. Original JS strings cross as UTF-16 units.
 */
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { syncBuiltinESMExports } from 'node:module'
import Database from 'better-sqlite3'

if (process.version !== 'v24.22.1') throw Error('Use pinned Node 24.22.1')
if (process.argv.slice(2).some((arg) => arg !== '--check')) throw Error('Only --check supported')
const sourcePath = 'src/secrets/store.ts'
const sourceHash = '489d4cda773e2c7ddc3062823f4e18f54bf818b66757da11015a918a2bb08393'
assert.equal(
  crypto
    .createHash('sha256')
    .update(readFileSync(new URL('../../' + sourcePath, import.meta.url)))
    .digest('hex'),
  sourceHash,
  'Review changed SecretStore oracle',
)
const key = Buffer.from(Array.from({ length: 32 }, (_, i) => i))
const nonce = Buffer.from(Array.from({ length: 12 }, (_, i) => i + 10))
const units = (s) => Array.from({ length: s.length }, (_, i) => s.charCodeAt(i))
const captured = (action) => {
  try {
    return { value: action() }
  } catch (error) {
    return { error: error.message }
  }
}
const originalRandomBytes = crypto.randomBytes
crypto.randomBytes = (length) => {
  if (length === 32) return Buffer.from(key)
  if (length === 12) return Buffer.from(nonce)
  throw Error('Unexpected synthetic randomBytes request')
}
syncBuiltinESMExports()
const { SecretStore } = await import('../../src/secrets/store.ts')
const db = new Database(':memory:')
try {
  // No keyPath: deterministic in-memory key generation, never real custody data.
  const store = new SecretStore(db)
  const select = db.prepare('SELECT ciphertext FROM secrets WHERE namespace=? AND name=?')
  const insert = db.prepare('INSERT OR REPLACE INTO secrets VALUES(?,?,?)')
  const canonical = [
    ['existing_vector', 'provider<>&\u2028中文', 'test"\\\n', 'synthetic fixture credential'],
    ['valid_pairs', 'space😀', 'name\udbff\udfff', 'synthetic😀\ud800\udc00\udbff\udfff'],
    ['lone_high', 'ns\ud800', 'name\ud801', 'synthetic\ud800'],
    ['lone_low', 'ns\udfff', 'name\udc00', 'synthetic\udfff'],
    ['reversed_surrogates', 'ns\udc00\ud800', '\udfff\ud801', '\udc00\ud800synthetic'],
    ['high_then_pair', '\ud800\ud801\udc02', 'n\ud800\ud800\udfff', 's\ud800\ud800\udfff'],
    ['replacement_character', 'ns\ufffd', 'name\ufffd', 'synthetic\ufffd'],
    ['nul', 'ns\0suffix', 'name\0suffix', 'synthetic\0value'],
    ['html_line_separators', '<script>&\u2028\u2029', '</script>\u2029\u2028', '<>&\u2028\u2029'],
    ['controls_quotes_slash', 'n\b\f\n\r\t', '"\\/\u0001\u001f', 's\b\f\n\r\t"\\/\u0001\u001f'],
    ['bom_noncharacters', '\ufeffnamespace', '\ufffe\uffff', '\ufeffsynthetic\ufffe\uffff'],
    ['no_normalization', 'é', 'e\u0301', 'é e\u0301'],
    ['empty_identity', '', '', 'synthetic'],
    ['whitespace_retained', ' namespace ', '\tname\n', ' synthetic\n\t '],
  ].map(([label, namespace, name, value]) => {
    store.apply(namespace, name, { action: 'set', value })
    const envelope = select.get(namespace, name).ciphertext
    const decrypted = store.get(namespace, name)
    assert.equal(decrypted, Buffer.from(value).toString('utf8'))
    return {
      label,
      namespaceUnits: units(namespace),
      nameUnits: units(name),
      valueUnits: units(value),
      inputJSON: JSON.stringify({ namespace, name, value }),
      aadHex: Buffer.from(JSON.stringify([namespace, name])).toString('hex'),
      plaintextHex: Buffer.from(value).toString('hex'),
      envelope,
      decrypted,
    }
  })
  const oldFixture = JSON.parse(
    readFileSync(new URL('../internal/credentials/testdata/node-v1.json', import.meta.url)),
  )
  assert.equal(canonical[0].envelope, oldFixture.envelope, 'Existing canonical v1 bytes changed')

  const namespace = 'synthetic:reader'
  const name = 'synthetic-token'
  const rawEnvelope = (plain, iv = nonce, tagLength = 16) => {
    const cipher = crypto.createCipheriv('aes-256-gcm', key, iv, { authTagLength: tagLength })
    cipher.setAAD(Buffer.from(JSON.stringify([namespace, name])))
    const ciphertext = Buffer.concat([cipher.update(plain), cipher.final()])
    return [
      'v1',
      iv.toString('base64'),
      cipher.getAuthTag().toString('base64'),
      ciphertext.toString('base64'),
    ].join('.')
  }
  const readEnvelope = (envelope) => {
    insert.run(namespace, name, envelope)
    return captured(() => store.get(namespace, name))
  }
  const byteCases = [
    ['ascii_nul', '610062'],
    ['bom', 'efbbbf61'],
    ['valid_pair', 'f09f9880'],
    ['replacement', 'efbfbd'],
    ['lone_continuations', '8081bf'],
    ['invalid_starters', 'c0c1f5ff'],
    ['truncated_two', 'c2'],
    ['truncated_three', 'e282'],
    ['truncated_four', 'f09f98'],
    ['interrupted_three', 'e28241'],
    ['interrupted_four', 'f09f9841'],
    ['surrogate_high', 'eda080'],
    ['surrogate_low', 'edbfbf'],
    ['surrogate_pair_bytes', 'eda080edb080'],
    ['overlong_three', 'e08080'],
    ['overlong_four', 'f0808080'],
    ['above_unicode', 'f4908080'],
    ['mixed_invalid_runs', '61e282ff808162f09f9843c2'],
  ].map(([label, hex]) => {
    const bytes = Buffer.from(hex, 'hex')
    const envelope = rawEnvelope(bytes)
    const result = readEnvelope(envelope)
    assert.deepEqual(result, { value: bytes.toString('utf8') })
    return { label, hex, envelope, ...result }
  })

  const baseline = rawEnvelope(Buffer.from('synthetic reader fixture'))
  const parts = baseline.split('.')
  const change = (index, update) =>
    parts.map((part, i) => (i === index ? update(part) : part)).join('.')
  const readerCases = []
  const readerCase = (label, envelope) =>
    readerCases.push({ label, envelope, ...readEnvelope(envelope) })
  readerCase('canonical', baseline)
  readerCase('extra_segment', baseline + '.ignored')
  readerCase('extra_empty_segment', baseline + '.')
  readerCase(
    'unpadded_base64',
    parts.map((part, i) => (i ? part.replace(/=+$/, '') : part)).join('.'),
  )
  readerCase(
    'base64url',
    parts
      .map((part, i) => (i ? Buffer.from(part, 'base64').toString('base64url') : part))
      .join('.'),
  )
  const urlAlphabetEnvelope = rawEnvelope(Buffer.from('synthetic fixture credential'))
  assert.match(urlAlphabetEnvelope, /[+/]/)
  readerCase(
    'base64url_padded_alphabet',
    urlAlphabetEnvelope.replaceAll('+', '-').replaceAll('/', '_'),
  )
  readerCase(
    'base64_spaces',
    change(3, (s) => ' ' + s.slice(0, 4) + ' \t' + s.slice(4) + ' '),
  )
  readerCase(
    'base64_crlf',
    change(3, (s) => '\r\n' + s.slice(0, 4) + '\r\n' + s.slice(4)),
  )
  readerCase(
    'base64_punctuation',
    change(3, (s) => '!' + s.slice(0, 4) + '!' + s.slice(4)),
  )
  readerCase(
    'base64_after_padding',
    change(2, (s) => s + 'ignored'),
  )
  readerCase(
    'base64_nonzero_pad_bits',
    change(2, (s) => {
      const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'
      return s.slice(0, -3) + alphabet[alphabet.indexOf(s.at(-3)) | 1] + '=='
    }),
  )
  readerCase('empty_plaintext', rawEnvelope(Buffer.alloc(0)))
  readerCase('missing_version', parts.slice(1).join('.'))
  readerCase('wrong_version', 'v2.' + parts.slice(1).join('.'))
  readerCase('missing_body', parts.slice(0, 3).join('.'))
  readerCase(
    'missing_iv',
    change(1, () => ''),
  )
  readerCase(
    'missing_tag',
    change(2, () => ''),
  )
  readerCase(
    'invalid_iv',
    change(1, () => '!'),
  )
  readerCase(
    'invalid_tag',
    change(2, () => '!'),
  )
  readerCase(
    'invalid_body',
    change(3, () => '!'),
  )
  readerCase(
    'tampered_body',
    change(3, (s) => {
      const b = Buffer.from(s, 'base64')
      b[0] ^= 1
      return b.toString('base64')
    }),
  )
  readerCase(
    'tampered_tag',
    change(2, (s) => {
      const b = Buffer.from(s, 'base64')
      b[0] ^= 1
      return b.toString('base64')
    }),
  )
  for (const length of [1, 8, 16, 32, 128]) {
    const iv = Buffer.from(Array.from({ length }, (_, i) => i))
    readerCase('nonce_' + length, rawEnvelope(Buffer.from('synthetic reader fixture'), iv))
  }
  for (const length of [0, 1, 4, 8, 11, 12, 13, 14, 15, 17]) {
    readerCase(
      'tag_' + length,
      change(2, (s) => {
        const original = Buffer.from(s, 'base64')
        return (
          length <= original.length
            ? original.subarray(0, length)
            : Buffer.concat([original, Buffer.of(0)])
        ).toString('base64')
      }),
    )
  }
  readerCase(
    'nonce_16_tag_12',
    rawEnvelope(Buffer.from('synthetic reader fixture'), Buffer.alloc(16, 7), 12),
  )

  // Compact oracle digests test replacement granularity without a huge JSON file.
  // Counts are test coverage only, not runtime admission limits.
  const digest = (produce) => {
    const hash = crypto.createHash('sha256')
    let count = 0
    const add = (bytes) => {
      const decoded = Buffer.from(bytes.toString('utf8'))
      const lengths = Buffer.alloc(8)
      lengths.writeUInt32BE(bytes.length, 0)
      lengths.writeUInt32BE(decoded.length, 4)
      hash.update(lengths).update(bytes).update(decoded)
      count++
    }
    produce(add)
    return { count, sha256: hash.digest('hex') }
  }
  const shortBytes = digest((add) => {
    add(Buffer.alloc(0))
    for (let a = 0; a < 256; a++) {
      add(Buffer.of(a))
      for (let b = 0; b < 256; b++) add(Buffer.of(a, b))
    }
  })
  const alphabet = [
    0, 0x41, 0x7f, 0x80, 0x8f, 0x90, 0x9f, 0xa0, 0xbf, 0xc0, 0xc1, 0xc2, 0xdf, 0xe0, 0xed, 0xef,
    0xf0, 0xf4, 0xf5, 0xff,
  ]
  const boundaries = digest((add) => {
    for (const a of alphabet)
      for (const b of alphabet)
        for (const c of alphabet) {
          add(Buffer.of(a, b, c))
          for (const d of alphabet) add(Buffer.of(a, b, c, d))
        }
  })
  const randomBytes = digest((add) => {
    let state = 0x6a09e667
    const random = () => {
      state ^= state << 13
      state ^= state >>> 17
      state ^= state << 5
      return state >>> 0
    }
    for (let i = 0; i < 4096; i++) {
      const bytes = Buffer.alloc(random() % 129)
      for (let j = 0; j < bytes.length; j++) bytes[j] = random() & 255
      add(bytes)
    }
  })
  const output =
    JSON.stringify(
      {
        purpose:
          'SYNTHETIC ONLY; fixed key and repeated nonce are test vectors, never production data',
        node: process.version,
        sourcePath,
        sourceHash,
        key: key.toString('hex'),
        nonce: nonce.toString('hex'),
        canonical,
        reader: { namespace, name, byteCases, cases: readerCases },
        utf8Oracle: { alphabet, shortBytes, boundaries, randomBytes },
      },
      null,
      2,
    ) + '\n'
  const target = new URL(
    '../internal/credentials/testdata/node-v1-byte-compat.json',
    import.meta.url,
  )
  if (process.argv.includes('--check'))
    assert.equal(readFileSync(target, 'utf8'), output, 'Credential byte fixtures changed')
  else writeFileSync(target, output)
  console.log(
    `${canonical.length} canonical SecretStore envelopes; ${byteCases.length} byte-decoding envelopes; ${readerCases.length} reader cases; ${shortBytes.count + boundaries.count + randomBytes.count} UTF-8 byte oracle inputs`,
  )
} finally {
  db.close()
  crypto.randomBytes = originalRandomBytes
  syncBuiltinESMExports()
}

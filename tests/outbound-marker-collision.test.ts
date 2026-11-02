import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { promisify } from 'node:util'
import test from 'node:test'

test('actual random marker collisions reject without exposing wire bytes or retaining reservations', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'outbound-marker-collision-'))
  try {
    const sentinel = '00000000-0000-4000-8000-000000000000'
    const preload = join(dir, 'fixed-uuid.cjs')
    const path = join(dir, 'original.bin')
    const bytes = Buffer.alloc(32, 42)
    await Promise.all([
      writeFile(path, bytes),
      writeFile(
        preload,
        `require('node:crypto').randomUUID = () => ${JSON.stringify(sentinel)};\nrequire('node:module').syncBuiltinESMExports();\n`,
      ),
    ])
    const moduleUrl = pathToFileURL(
      join(process.cwd(), 'src/plugins/builtin/snowluma/outbound.ts'),
    ).href
    const source = {
      index: 0,
      path,
      bytes: bytes.length,
      hash: createHash('sha256').update(bytes).digest('hex'),
    }
    const { stdout } = await promisify(execFile)(
      process.execPath,
      [
        '--require',
        preload,
        '--import',
        'tsx',
        '--input-type=module',
        '-e',
        `
          import assert from 'node:assert/strict';
          import { randomUUID } from 'node:crypto';
          import { OutboundEncoder, outboundEncodingUsage } from ${JSON.stringify(moduleUrl)};
          assert.equal(randomUUID(), ${JSON.stringify(sentinel)});
          const source = ${JSON.stringify(source)};
          const marker = '__humanengine_original_' + randomUUID() + '__';
          const image = () => ({ type: 'image', data: { file: '' } });
          const encoder = new OutboundEncoder();
          let exposed = false;
          try {
            // A successful control rules out a broken worker/import/path as the rejection cause.
            const control = await encoder.encode({ message: [image()] }, [source]);
            assert.equal(JSON.parse(Buffer.from(control.bytes).toString()).message[0].data.file,
              ${JSON.stringify(`base64://${bytes.toString('base64')}`)});
            control.release();
            for (const [value, sources] of [
              [{ note: marker, message: [image()] }, [source]],
              [{ message: [image(), image()] }, [source, { ...source, index: 1 }]],
            ]) {
              await assert.rejects(async () => {
                const lease = await encoder.encode(value, sources);
                exposed = true;
                lease.release();
              }, /outbound_encoding_failed/);
              assert.equal(exposed, false);
              assert.equal(outboundEncodingUsage().jobs, 0);
              assert.equal(outboundEncodingUsage().reservedBytes, 0);
            }
            console.log(JSON.stringify({ cases: 2, exposed, jobs: 0, reservedBytes: 0 }));
          } finally { encoder.dispose(); }
        `,
      ],
      { timeout: 5000, maxBuffer: 64 * 1024 },
    )
    assert.deepEqual(JSON.parse(stdout), { cases: 2, exposed: false, jobs: 0, reservedBytes: 0 })
  } finally {
    // The crypto override only exists in the child and its worker; child exit restores nothing globally.
    await rm(dir, { recursive: true, force: true })
  }
})

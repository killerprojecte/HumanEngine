// Regenerate synthetic source traces outside the checkout; never overwrite fixtures.
import { mkdtempSync, readFileSync, copyFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { resolve, join } from 'node:path';
import { spawnSync } from 'node:child_process';
const root = resolve(process.env.HUMANENGINE_SOURCE_ROOT || process.cwd());
const temporary = mkdtempSync(join(tmpdir(), 'humanengine-meme-oracle-'));
const fixtures = join(root, 'backend/internal/memeservice/testdata');
try {
  for (const script of ['oracle.mjs', 'live-oracle.mjs', 'durable-oracle.mjs']) {
    const target = join(temporary, script);
    copyFileSync(join(fixtures, script), target);
    const result = spawnSync(process.execPath, ['--import', 'tsx', target, join(root, 'src/memes.ts')], {
      cwd: root, env: { ...process.env, HE_SOURCE_ROOT: root, TZ: 'UTC' }, encoding: 'utf8', maxBuffer: 16 * 1024 * 1024,
    });
    if (result.error) throw result.error;
    if (result.status !== 0) throw new Error(`${script} failed (${result.status}): ${result.stderr}\n${result.stdout}`);
    if (script === 'oracle.mjs') writeFileSync(join(temporary, 'oracle.json'), result.stdout);
  }
  for (const file of ['oracle.json', 'live-oracle.json', 'durable-oracle.json']) {
    if (!readFileSync(join(fixtures, file)).equals(readFileSync(join(temporary, file)))) throw new Error(`actual-source Meme oracle differs: ${file}`);
  }
  console.log('PASS: actual-source Meme tags/concepts, live service and split durable traces');
} finally {
  rmSync(temporary, { recursive: true, force: true });
}

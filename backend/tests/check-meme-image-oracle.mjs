// Real pinned Sharp fixture regeneration uses only generated synthetic pixels.
import { mkdtempSync, copyFileSync, readFileSync, rmSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { spawnSync } from 'node:child_process';
const root = resolve(process.env.HUMANENGINE_SOURCE_ROOT || process.cwd());
const fixtures = join(root, 'backend/internal/imagecodec/testdata');
const temporary = mkdtempSync(join(tmpdir(), 'humanengine-meme-sharp-'));
try {
  const script = join(temporary, 'meme-sharp.mjs');
  copyFileSync(join(fixtures, 'meme-sharp.mjs'), script);
  const result = spawnSync(process.execPath, [script], { cwd: root, env: { ...process.env, HE_SOURCE_ROOT: root, VIPS_CONCURRENCY: '1' }, encoding: 'utf8' });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`Meme Sharp generator failed (${result.status}): ${result.stderr}`);
  if (!readFileSync(join(fixtures, 'meme-sharp.json')).equals(readFileSync(join(temporary, 'meme-sharp.json')))) throw new Error('actual Sharp Meme fixture drift');
  console.log('PASS: 21 actual Sharp metadata/frame/thumbnail fixtures');
} finally { rmSync(temporary, { recursive: true, force: true }); }

// Regenerate actual pinned TypeScript results in a fresh directory, never in the checkout.
import { mkdtempSync, readFileSync, copyFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { resolve, join } from 'node:path';
import { spawnSync } from 'node:child_process';

const root = resolve(process.env.HUMANENGINE_SOURCE_ROOT || process.cwd());
const fixtures = join(root, 'backend/internal/experienceservice/testdata');
const temporary = mkdtempSync(join(tmpdir(), 'humanengine-experience-oracle-'));
const scripts = ['generate-domain-oracle.mjs', 'service-oracle.mjs', 'reference-oracle.mjs', 'presence-oracle.mjs'];
try {
  for (const script of scripts) {
    const target = join(temporary, script);
    copyFileSync(join(fixtures, script), target);
    const result = spawnSync(process.execPath, ['--experimental-transform-types', target, root, temporary], {
      cwd: root, env: { ...process.env, TZ: 'UTC' }, encoding: 'utf8',
    });
    if (result.error) throw result.error;
    if (result.status !== 0) throw new Error(`${script} failed (${result.status}): ${result.stderr}\n${result.stdout}`);
  }
  for (const file of ['domain-oracle.json', 'behavior-holdouts.json', 'service-oracle.json', 'reference-oracle.json', 'presence-oracle.json']) {
    if (!readFileSync(join(fixtures, file)).equals(readFileSync(join(temporary, file)))) {
      throw new Error(`actual-source Experience oracle differs: ${file}`);
    }
  }
  console.log('PASS: actual-source Experience domain, service and 112-case reference matrix');
} finally {
  rmSync(temporary, { recursive: true, force: true });
}

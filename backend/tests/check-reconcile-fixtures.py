"""Regenerate actual recovery owners and whole-runner oracles privately."""
import json
import os
from pathlib import Path
import subprocess
import tempfile

root = Path(__file__).resolve().parents[2]
backend = root / 'backend'
with tempfile.TemporaryDirectory(prefix='humanengine-reconcile-oracle-') as directory:
    tmp = Path(directory)
    env = dict(os.environ, HE_SOURCE_ROOT=str(root), HUMANENGINE_SOURCE_ROOT=str(root), TZ='UTC')
    pairs = []
    for script, name in [('recovery-domain-oracle.mjs', 'domains-node.json'),
                         ('recovery-domain-source-oracle.mjs', 'domain-source-node.json'),
                         ('recovery-trials-oracle.mjs', 'trials-node.json')]:
        output = subprocess.check_output(['node', '--import', 'tsx', str(backend / 'scripts' / script)], cwd=root, env=env)
        (tmp / name).write_bytes(output)
        pairs.append((tmp / name, backend / 'internal/recoverydomains/testdata' / name))
    subprocess.run(['node', '--import', 'tsx', str(backend / 'tests/generate-reconcile-fixtures.mjs')],
                   cwd=root, env=dict(env, HUMANENGINE_FIXTURE_OUTPUT=str(tmp / 'reconcile.json'),
                                      HUMANENGINE_ORACLE_WORK=str(tmp), HUMANENGINE_DOMAIN_FIXTURES=str(tmp)), check=True)
    pairs.append((tmp / 'reconcile.json', backend / 'internal/offlinelifecycle/testdata/reconcile.json'))
    for actual, expected in pairs:
        if json.loads(actual.read_text()) != json.loads(expected.read_text()):
            raise SystemExit(f'Actual-source fixture drift: {expected.relative_to(root)}')
print('PASS: actual domain, source, trial and 20-scenario reconciliation fixtures match')

"""Regenerate actual TypeScript provider oracles in a private directory."""
import json
import os
from pathlib import Path
import subprocess
import tempfile

root = Path(__file__).resolve().parents[2]
backend = root / 'backend'
node = ['node', '--import', 'tsx']
with tempfile.TemporaryDirectory(prefix='humanengine-provider-oracle-') as tmp:
    tmp = Path(tmp)
    env = dict(os.environ, HUMANENGINE_SOURCE_ROOT=str(root), TZ='UTC')
    subprocess.run(node + [str(backend / 'internal/providerwire/testdata/generate-source.mts'), str(root), str(tmp)], cwd=root, env=env, check=True)
    pairs = [(tmp / name, backend / 'internal/providerwire/testdata' / name) for name in ('source.json', 'stream-source.json', 'usage-source.json')]
    for script, name in [('generate-provider-transport-fixtures.mjs', 'source-http.json'), ('generate-provider-review-fixtures.mjs', 'review-source.json')]:
        subprocess.run(node + [str(backend / 'tests' / script)], cwd=root, env=dict(env, HUMANENGINE_FIXTURE_OUTPUT=str(tmp / name)), check=True)
        pairs.append((tmp / name, backend / 'internal/providertransport/testdata' / name))
    output = subprocess.check_output(node + [str(backend / 'tests/generate-budget-ledger.mjs'), str(root)], cwd=root, env=env)
    (tmp / 'budget-ledger.json').write_bytes(output)
    pairs.append((tmp / 'budget-ledger.json', backend / 'internal/providertransport/testdata/budget-ledger.json'))
    for actual, expected in pairs:
        if json.loads(actual.read_text()) != json.loads(expected.read_text()):
            raise SystemExit(f'Actual-source fixture drift: {expected.relative_to(root)}')
print('PASS: all six actual-source provider fixture documents match')

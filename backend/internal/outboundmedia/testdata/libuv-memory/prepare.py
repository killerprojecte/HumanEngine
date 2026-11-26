#!/usr/bin/env python3
"""Prepare a tiny harness input, verifying the cached official source extract."""
import hashlib
import json
import pathlib
import sys
root = pathlib.Path(__file__).resolve().parent
source_root = pathlib.Path(sys.argv[1])
out = pathlib.Path(sys.argv[2])
for line in (root / 'UPSTREAM.sha256').read_text().splitlines():
    digest, relative = line.split('  ', 1)
    data = (source_root / relative).read_bytes()
    assert hashlib.sha256(data).hexdigest() == digest, relative
linux = (source_root / 'node-v24.22.1/deps/uv/src/unix/linux.c').read_text()
start = linux.index('static uint64_t uv__read_proc_meminfo(')
end = linux.index('static int uv__get_cgroupv2_constrained_cpu(', start)
assert (root / 'source.inc').read_text() == linux[start:end]
cases = json.loads((root / 'fixtures.json').read_text())
lines = []
for i, case in enumerate(cases):
    lines.append(f'static const struct file_entry files_{i}[] = {{')
    lines.extend('  {' + json.dumps(path) + ', ' + json.dumps(contents) + '},' for path, contents in case['Files'].items())
    lines.append('};')
lines.append('static const struct fixture fixtures[] = {')
for i, case in enumerate(cases):
    lines.append('  {' + json.dumps(case['Name']) + f', files_{i}, sizeof(files_{i})/sizeof(files_{i}[0]), UINT64_C({case["SysFree"]}), UINT64_C({case["SysTotal"]}), {int(case["SysOK"])}, UINT64_C({case["Want"]})' + '},')
lines.append('};')
out.mkdir(parents=True, exist_ok=True)
(out / 'fixtures.h').write_text('\n'.join(lines) + '\n')
print(f'Verified 4 official source hashes and prepared {len(cases)} libuv fixtures.')

#!/usr/bin/env python3
"""Stage/verify the official locked SQLite extension. No network or subprocesses.

Run after npm ci. Dependency installation remains the lockfile's responsibility;
this step additionally pins the exact native bytes before Go embeds them.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import tempfile

parser = argparse.ArgumentParser()
parser.add_argument('--repository', type=Path, help='repository containing package-lock.json and node_modules')
parser.add_argument('--asset-directory', type=Path, default=Path(__file__).resolve().parents[1] / 'internal/vectorasset')
parser.add_argument('--target', default='linux/amd64')
parser.add_argument('--check', action='store_true', help='verify, do not replace staged binary')
args = parser.parse_args()
asset = args.asset_directory.resolve()
manifest = json.loads((asset / 'manifest.json').read_text())
if args.target != manifest['target']:
    raise SystemExit(f'unsupported sqlite-vec target: {args.target}; no verified artifact')

def verify(data):
    if len(data) != manifest['binarySize'] or hashlib.sha256(data).hexdigest() != manifest['binarySHA256']:
        raise SystemExit('sqlite-vec binary does not match pinned bytes')
    if data[:6] != b'\x7fELF\x02\x01' or int.from_bytes(data[18:20], 'little') != 62:
        raise SystemExit('sqlite-vec binary is not ELF64 little-endian x86-64')

# The runtime digest/version constants must agree with the reviewed provenance.
source = (asset / 'asset.go').read_text()
for value in [manifest['binarySHA256'], manifest['version']]:
    if f'"{value}"' not in source:
        raise SystemExit('runtime constants differ from the reviewed manifest')
if not (asset / 'LICENSE-MIT').read_text().startswith('MIT License\n\nCopyright (c) 2024 Alex Garcia\n'):
    raise SystemExit('missing upstream license')
target = asset / manifest['binary']
if args.repository:
    repo = args.repository.resolve()
    lock = json.loads((repo / 'package-lock.json').read_text())['packages']
    entry = lock['node_modules/' + manifest['package']]
    for field, want in [('version', manifest['packageVersion']), ('resolved', manifest['npmResolved']), ('integrity', manifest['npmIntegrity'])]:
        if entry.get(field) != want:
            raise SystemExit(f'lockfile sqlite-vec {field} changed; explicit review required')
    if lock['node_modules/sqlite-vec']['optionalDependencies'].get(manifest['package']) != manifest['packageVersion']:
        raise SystemExit('parent sqlite-vec package dependency changed')
    package = repo / 'node_modules' / manifest['package']
    metadata = json.loads((package / 'package.json').read_text())
    if metadata['name'] != manifest['package'] or metadata['version'] != manifest['packageVersion']:
        raise SystemExit('installed sqlite-vec package does not match lock')
    data = (package / 'vec0.so').read_bytes()
    verify(data)
    if not args.check:
        target.parent.mkdir(parents=True, exist_ok=True)
        fd, temporary = tempfile.mkstemp(prefix='.vec0-', dir=target.parent)
        try:
            with os.fdopen(fd, 'wb') as output:
                output.write(data)
                output.flush()
                os.fsync(output.fileno())
            os.chmod(temporary, 0o644)
            os.replace(temporary, target)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)
verify(target.read_bytes())
print(f"Verified genuine sqlite-vec {manifest['version']} {args.target}: {manifest['binarySHA256']}")

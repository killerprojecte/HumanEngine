#!/usr/bin/env python3
"""Retrieve the exact official Node source archive; never install or compile it."""
import argparse
import hashlib
import json
import os
import pathlib
import tempfile
from urllib.request import urlopen

HERE = pathlib.Path(__file__).resolve().parent


def sha(path):
    with path.open('rb') as source:
        return hashlib.file_digest(source, 'sha256').hexdigest()


def fetch(provenance, destination, opener=urlopen):
    expected = provenance['archiveSHA256']
    if destination.exists():
        if sha(destination) != expected:
            raise ValueError('existing Node source archive checksum mismatch')
        print('Verified existing pinned Node archive:', destination)
        return
    # Both the checked-in pin and current official checksum must agree before
    # downloading. A vendor endpoint change never silently updates our pin.
    filename = provenance['sourceArchive'].rsplit('/', 1)[1]
    with opener(provenance['officialChecksums']) as response:
        records = [line.split() for line in response.read().decode('ascii').splitlines()]
    matches = [row[0] for row in records if len(row) == 2 and row[1] == filename]
    if matches != [expected]:
        raise ValueError('official Node checksums do not match pinned source archive')
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='.download-', dir=destination.parent) as temp:
        pending = pathlib.Path(temp) / filename
        digest = hashlib.sha256()
        with opener(provenance['sourceArchive']) as response, pending.open('wb') as output:
            while chunk := response.read(1024 * 1024):
                digest.update(chunk)
                output.write(chunk)
        if digest.hexdigest() != expected:
            raise ValueError('downloaded Node source archive checksum mismatch')
        os.replace(pending, destination)
    print('Verified official Node source archive:', destination)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--archive', required=True, type=pathlib.Path)
    args = parser.parse_args()
    fetch(json.loads((HERE / 'provenance.json').read_text()), args.archive.resolve())


if __name__ == '__main__':
    main()

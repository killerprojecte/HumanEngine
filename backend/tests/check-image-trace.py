"""Check a standalone image probe's strace file-access/process evidence."""
import re
import sys
from pathlib import Path


def check(text, source_root, env_binary):
    if source_root in text or 'node_modules' in text:
        raise ValueError('development source/dependency lookup in runtime trace')
    calls = []
    for line in text.splitlines():
        if re.match(r'^(?:\d+\s+)?execveat\(', line):
            raise ValueError('unexpected execveat in standalone runtime')
        if re.match(r'^(?:\d+\s+)?execve\(', line):
            match = re.search(r'execve\("([^"\n]+)"', line)
            if not match:
                raise ValueError('unrecognized process trace')
            calls.append(match.group(1))
    if calls != [env_binary, './imagecodec-probe']:
        raise ValueError(f'unexpected standalone process execution: {calls!r}')


if __name__ == '__main__':
    check(Path(sys.argv[1]).read_text(), sys.argv[2], sys.argv[3])

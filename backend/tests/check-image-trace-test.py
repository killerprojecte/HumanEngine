"""Synthetic parser controls, not a replacement for real ptrace evidence."""
import importlib.util
from pathlib import Path

spec = importlib.util.spec_from_file_location('image_trace', Path(__file__).with_name('check-image-trace.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
valid = '1 execve("/usr/bin/env", [], []) = 0\n1 execve("./imagecodec-probe", [], []) = 0\n2 openat(AT_FDCWD, "/lib/libc.so.6", O_RDONLY) = 3\n'
module.check(valid, '/checkout', '/usr/bin/env')
for bad in [valid + '2 openat(AT_FDCWD, "/checkout/src/image-upload.ts", O_RDONLY) = 3',
            valid + '2 openat(AT_FDCWD, "/other/node_modules/x", O_RDONLY) = 3',
            valid + '2 execve("/usr/bin/node", [], []) = 0',
            valid + '2 execveat(3, "", [], [], AT_EMPTY_PATH) = 0',
            '', valid.replace('./imagecodec-probe', './other')]:
    try:
        module.check(bad, '/checkout', '/usr/bin/env')
    except ValueError:
        continue
    raise AssertionError('invalid trace was accepted')
print('PASS: standalone trace parser controls')

"""Small builder tests: no ICU extraction, native compilation, or network."""
import importlib.util
import json
import pathlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('icu_build', pathlib.Path(__file__).with_name('build.py'))
build = importlib.util.module_from_spec(spec)
spec.loader.exec_module(build)


class BuilderTests(unittest.TestCase):
    def setUp(self):
        # Always workspace-owned, including on hosts with a small shared /tmp.
        self.temp = tempfile.TemporaryDirectory(prefix='.builder-test-', dir=pathlib.Path(__file__).parent)
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.out = self.root / 'common_test.o'
        self.stamp = self.out.with_suffix('.key')
        self.command = ['compiler', '-c', 'source.cpp', '-o', str(self.out)]

    def compiler(self, content=b'complete-object'):
        def run(command):
            path = pathlib.Path(command[command.index('-o') + 1])
            self.assertNotEqual(path, self.out)
            path.write_bytes(content)
        return run

    def test_key_includes_all_verified_headers_and_sources(self):
        original = build.object_key('cpp', 'manifest-with-header-v1', 'compiler', self.command)
        self.assertEqual(original, build.object_key('cpp', 'manifest-with-header-v1', 'compiler', self.command))
        self.assertNotEqual(original, build.object_key('cpp', 'manifest-with-header-v2', 'compiler', self.command))
        self.assertNotEqual(original, build.object_key('cpp', 'manifest-with-header-v1', 'new-compiler', self.command))

    def test_success_and_cache_hit(self):
        build.compile_object(self.command, self.out, 'key', self.compiler())
        self.assertTrue(build.cached_object(self.out, self.stamp, 'key'))
        def unexpected(_):
            self.fail('cache hit invoked compiler')
        self.assertIsNone(build.compile_object(self.command, self.out, 'key', unexpected))
        self.assertFalse(build.cached_object(self.out, self.stamp, 'changed-manifest-key'))

    def test_partial_compile_preserves_old_object_and_stamp(self):
        build.compile_object(self.command, self.out, 'old-key', self.compiler(b'old-object'))
        old_stamp = self.stamp.read_bytes()
        def fail(command):
            self.compiler(b'partial-object')(command)
            raise RuntimeError('compiler interrupted')
        with self.assertRaises(RuntimeError):
            build.compile_object(self.command, self.out, 'new-key', fail)
        self.assertEqual(self.out.read_bytes(), b'old-object')
        self.assertEqual(self.stamp.read_bytes(), old_stamp)
        self.assertTrue(build.cached_object(self.out, self.stamp, 'old-key'))
        self.assertEqual(list(self.root.glob('.compile-*')), [])

    def test_interruption_between_object_and_stamp_does_not_hit_old_key(self):
        build.compile_object(self.command, self.out, 'old-key', self.compiler(b'old-object'))
        replace = build.os.replace
        def interrupt_stamp(src, dst):
            if dst == self.stamp:
                raise RuntimeError('interrupted before stamp publication')
            replace(src, dst)
        with patch.object(build.os, 'replace', side_effect=interrupt_stamp):
            with self.assertRaises(RuntimeError):
                build.compile_object(self.command, self.out, 'new-key', self.compiler(b'new-object'))
        self.assertEqual(self.out.read_bytes(), b'new-object')
        self.assertFalse(build.cached_object(self.out, self.stamp, 'old-key'))
        self.assertFalse(build.cached_object(self.out, self.stamp, 'new-key'))
        build.compile_object(self.command, self.out, 'new-key', self.compiler(b'new-object'))
        self.assertTrue(build.cached_object(self.out, self.stamp, 'new-key'))

    def test_corrupt_object_and_legacy_stamp_are_misses(self):
        build.compile_object(self.command, self.out, 'key', self.compiler())
        self.out.write_bytes(b'partial')
        self.assertFalse(build.cached_object(self.out, self.stamp, 'key'))
        self.stamp.write_text('key')
        self.assertFalse(build.cached_object(self.out, self.stamp, 'key'))
        self.stamp.write_text(json.dumps({'key': 'key'}))
        self.assertFalse(build.cached_object(self.out, self.stamp, 'key'))

    def test_data_object_is_published_atomically_without_cache(self):
        self.out.write_bytes(b'old-data')
        build.compile_object(self.command, self.out, runner=self.compiler(b'new-data'))
        self.assertEqual(self.out.read_bytes(), b'new-data')
        self.assertFalse(self.stamp.exists())

    def test_fresh_archive_drops_removed_members(self):
        # ar accepts arbitrary regular files: these tiny fixtures require no C++.
        old, keep = self.root/'removed.o', self.root/'retained.o'
        old.write_bytes(b'old'); keep.write_bytes(b'keep')
        lib = self.root/'libtest.a'
        subprocess.run(['ar', 'rcsD', str(lib), str(old), str(keep)], check=True)
        build.build_archive('ar', [keep], lib)
        members = subprocess.check_output(['ar', 't', str(lib)], text=True).splitlines()
        self.assertEqual(members, ['retained.o'])
        self.assertEqual(list(self.root.glob('.archive-*')), [])

    def test_failed_archive_preserves_existing(self):
        lib = self.root/'libtest.a'
        lib.write_bytes(b'old-archive')
        def fail(command):
            target = pathlib.Path(command[2])
            self.assertFalse(target.exists())
            target.write_bytes(b'partial-archive')
            raise RuntimeError('archiver interrupted')
        with self.assertRaises(RuntimeError):
            build.build_archive('ar', [], lib, fail)
        self.assertEqual(lib.read_bytes(), b'old-archive')
        self.assertEqual(list(self.root.glob('.archive-*')), [])


if __name__ == '__main__':
    unittest.main()

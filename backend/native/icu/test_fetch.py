"""Pinned downloader tests use in-memory responses, never network access."""
import hashlib
import importlib.util
import io
import pathlib
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('icu_fetch', pathlib.Path(__file__).with_name('fetch.py'))
fetcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fetcher)


class FetchTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='.fetch-test-', dir=pathlib.Path(__file__).parent)
        self.addCleanup(self.temp.cleanup)
        self.target = pathlib.Path(self.temp.name) / 'node.tar.xz'
        self.data = b'synthetic archive bytes'
        self.digest = hashlib.sha256(self.data).hexdigest()
        self.p = {'archiveSHA256': self.digest, 'sourceArchive': 'https://nodejs.org/dist/pinned/node.tar.xz',
                  'officialChecksums': 'https://nodejs.org/dist/pinned/SHASUMS256.txt'}
        self.responses = {self.p['sourceArchive']: self.data,
                          self.p['officialChecksums']: (self.digest + '  node.tar.xz\n').encode()}
        self.requests = []

    def open(self, url):
        self.requests.append(url)
        return io.BytesIO(self.responses[url])

    def test_verified_atomic_download(self):
        fetcher.fetch(self.p, self.target, self.open)
        self.assertEqual(self.target.read_bytes(), self.data)
        self.assertEqual(self.requests, [self.p['officialChecksums'], self.p['sourceArchive']])
        self.assertEqual(list(self.target.parent.glob('.download-*')), [])

    def test_existing_valid_archive_needs_no_network(self):
        self.target.write_bytes(self.data)
        fetcher.fetch(self.p, self.target, self.open)
        self.assertEqual(self.requests, [])

    def test_existing_invalid_archive_is_not_overwritten(self):
        self.target.write_bytes(b'wrong')
        with self.assertRaisesRegex(ValueError, 'existing'):
            fetcher.fetch(self.p, self.target, self.open)
        self.assertEqual(self.target.read_bytes(), b'wrong')
        self.assertEqual(self.requests, [])

    def test_changed_official_checksum_stops_before_archive_download(self):
        self.responses[self.p['officialChecksums']] = b'wrong  node.tar.xz\n'
        with self.assertRaisesRegex(ValueError, 'official'):
            fetcher.fetch(self.p, self.target, self.open)
        self.assertEqual(self.requests, [self.p['officialChecksums']])
        self.assertFalse(self.target.exists())

    def test_invalid_archive_is_never_published(self):
        self.responses[self.p['sourceArchive']] = b'wrong'
        with self.assertRaisesRegex(ValueError, 'downloaded'):
            fetcher.fetch(self.p, self.target, self.open)
        self.assertFalse(self.target.exists())
        self.assertEqual(list(self.target.parent.glob('.download-*')), [])


if __name__ == '__main__':
    unittest.main()

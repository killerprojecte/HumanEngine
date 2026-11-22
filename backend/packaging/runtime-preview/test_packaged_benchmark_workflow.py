import unittest
from pathlib import Path

from migration_drill import TS_REVISION

ROOT = Path(__file__).resolve().parents[3]


class BenchmarkWorkflowTests(unittest.TestCase):
    def test_narrow_branch_only_push_and_pinned_local_builds(self):
        workflow = (ROOT / ".github/workflows/golang-package-benchmark.yml").read_text()
        trigger, job = workflow.split("permissions:", 1)
        paths = [line.strip().split("'")[1] for line in trigger.splitlines() if line.strip().startswith("- '")]
        self.assertEqual(paths, [
            "backend/packaging/runtime-preview/backend_benchmark.py",
            "backend/packaging/runtime-preview/packaged_benchmark.py",
            ".github/workflows/golang-package-benchmark.yml",
        ])
        self.assertIn("branches: [feat/refactor-golang]", trigger)
        self.assertIn("workflow_dispatch:", trigger)
        self.assertIn("contents: read", job)
        self.assertIn("runs-on: ubuntu-24.04", job)
        self.assertIn("ref: " + TS_REVISION, job)
        go_build = 'SOURCE_REVISION="$GITHUB_SHA" -f backend/packaging/runtime-preview/Dockerfile'
        self.assertIn(go_build, job)
        self.assertLess(job.index(go_build), job.index("path: .benchmark-typescript"))
        self.assertIn("--target build", job)
        self.assertIn('--go-revision "$GITHUB_SHA"', job)
        self.assertIn('path: ${{ runner.temp }}/humanengine-paired-http.json', job)
        self.assertNotRegex(job, r"docker (push|login)|push: true|permissions: write")


if __name__ == "__main__":
    unittest.main()

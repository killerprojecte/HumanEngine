"""Driver tests only; these never replace the actual Docker acceptance gate."""
import io
from pathlib import Path
import unittest
from unittest.mock import patch

import acceptance


class DriverTests(unittest.TestCase):
    def test_node_process_check_requests_pid_and_parses_command_column(self):
        for rows, expected in [
            ("PID                 COMMAND\n1421                humanengine", True),
            ("PID COMMAND\n1421 humanengine\n1530 node", False),
            (" PID   COMMAND\n1421 humanengine\n1531 nodejs", False),
            ("PID COMMAND\n1421 node-exporter", True),
        ]:
            with self.subTest(rows=rows), patch.object(acceptance, "docker", return_value=rows) as call:
                self.assertEqual(acceptance.no_node("fixture"), expected)
                call.assert_called_once_with("top", "fixture", "-eo", "pid,comm")
        with patch.object(acceptance, "docker", return_value="COMMAND\nnode"), self.assertRaises(AssertionError):
            acceptance.no_node("fixture")

    def test_packaging_uses_modern_python_distro_and_matching_native_runtime(self):
        # Source contract only: actual libraries still require the Docker gate.
        dockerfile = Path(__file__).with_name("Dockerfile").read_text()
        bases = [line.split()[1] for line in dockerfile.splitlines() if line.startswith("FROM ")]
        self.assertEqual(bases, ["golang:1.27.3-trixie", "node:24.22.1-trixie-slim",
                                 "node:24.22.1-trixie-slim"])
        self.assertIn("python3 make g++", dockerfile)
        builder = Path(__file__).resolve().parents[2] / "native/icu/build.py"
        self.assertIn("filter='data'", builder.read_text())

    def test_continue_requires_complete_response_before_signal(self):
        acceptance.read_continue(io.BytesIO(b"HTTP/1.1 100 Continue\r\n\r\n"))
        acceptance.read_continue(io.BytesIO(b"HTTP/1.1 100 Continue\r\nX-Test: yes\r\n\r\n"))
        for response in (b"", b"HTTP/1.1 401 Unauthorized\r\n\r\n",
                         b"HTTP/1.1 100 Continue\r\nX-Test: yes\r\n"):
            with self.subTest(response=response), self.assertRaises(AssertionError):
                acceptance.read_continue(io.BytesIO(response))

    def test_deadline_is_failure_not_success(self):
        with patch.object(acceptance.time, "monotonic", side_effect=[0, 1]), self.assertRaises(AssertionError):
            acceptance.wait_for(lambda: False, "fixture", seconds=1)

    def test_ready_value_is_returned(self):
        self.assertEqual(acceptance.wait_for(lambda: "ready", "fixture"), "ready")

    def test_http_request_keeps_token_out_of_url_and_checks_status(self):
        with patch.object(acceptance.http.client, "HTTPConnection") as constructor:
            connection = constructor.return_value
            response = connection.getresponse.return_value
            response.status = 200
            response.read.return_value = b'{"saved":true}'
            self.assertEqual(acceptance.request(1234, "PUT", "/api/settings", {"debugModelIO": False}), {"saved": True})
            args = connection.request.call_args.args
            self.assertEqual(args[:2], ("PUT", "/api/settings"))
            self.assertEqual(args[3]["Authorization"], "Bearer " + acceptance.TOKEN)
            connection.close.assert_called_once()
            response.status = 503
            with self.assertRaises(AssertionError):
                acceptance.request(1234, "GET", "/api/settings")

    def test_unauthenticated_probe_omits_header(self):
        with patch.object(acceptance.http.client, "HTTPConnection") as constructor:
            connection = constructor.return_value
            connection.getresponse.return_value.status = 401
            connection.getresponse.return_value.read.return_value = b'{}'
            acceptance.request(1234, "GET", "/api/settings", authenticated=False, expected=401)
            self.assertNotIn("Authorization", connection.request.call_args.args[3])


if __name__ == "__main__":
    unittest.main()

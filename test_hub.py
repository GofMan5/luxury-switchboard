import http.client
import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path


ROOT = Path(__file__).parent
MODULE_PATH = ROOT / "deploy" / "tunnel_hub.py"
SPEC = importlib.util.spec_from_file_location("tunnel_hub", MODULE_PATH)
hub = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(hub)


class TunnelHubTests(unittest.TestCase):
    SELF_ID = "tunnel_a1b2c3d4"
    OTHER_ID = "tunnel_e5f6g7h8"

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.state_path = Path(self.temporary.name) / "tunnels.json"
        self.state_path.write_text(
            json.dumps(
                {
                    "v": 2,
                    "revision": 7,
                    "tunnels": [
                        {
                            "id": self.OTHER_ID,
                            "owner": "owner-2",
                            "state": "running",
                        },
                        {
                            "id": self.SELF_ID,
                            "owner": "owner-1",
                            "state": "running",
                        },
                    ],
                }
            ),
            encoding="utf-8",
        )

    def tearDown(self):
        self.temporary.cleanup()

    def response(self, command, owner="owner-1"):
        return json.loads(hub.control(command, self.state_path, owner))

    def test_shared_protocol_is_strict_filtered_revisioned_and_idempotent(self):
        listed = self.response("v1 list")
        self.assertEqual(
            set(listed), {"v", "ok", "revision", "tunnels"}
        )
        self.assertEqual(
            listed["tunnels"],
            [
                {"name": "Ваш коннект", "state": "running"},
                {"name": "Tunnel 1", "state": "running"},
            ],
        )
        serialized = json.dumps(listed).lower()
        for private in (
            "owner",
            "id",
            "slug",
            "url",
            "port",
            "provider",
            "model",
            "key",
            "proxy",
            self.SELF_ID,
            self.OTHER_ID,
            "owner-1",
            "owner-2",
        ):
            self.assertNotIn(private, serialized)

        owner_two = self.response("v1 list", "owner-2")
        self.assertEqual(
            owner_two["tunnels"],
            [
                {"name": "Ваш коннект", "state": "running"},
                {"name": "Tunnel 1", "state": "running"},
            ],
        )

        paused = self.response("v1 pause 7 1")
        self.assertEqual((paused["revision"], paused["tunnels"][1]["state"]), (8, "paused"))
        state, _migrated = hub._read_state(self.state_path)
        other = next(tunnel for tunnel in state["tunnels"] if tunnel["id"] == self.OTHER_ID)
        self.assertEqual(other["state"], "paused")
        repeated = self.response("v1 pause 8 1")
        self.assertEqual(repeated["revision"], 8)
        resumed = self.response("v1 resume 8 1")
        self.assertEqual((resumed["revision"], resumed["tunnels"][1]["state"]), (9, "running"))
        stopped = self.response("v1 stop 9 1", "owner-2")
        self.assertEqual((stopped["revision"], stopped["tunnels"][1]["state"]), (10, "stopped"))

        for invalid in (
            "",
            "list",
            "v1  list",
            f"v1 pause {self.SELF_ID}",
            "v1 pause 10 1 ",
            "v1 start 10 1",
            "v1 pause 9 1",
            "v1 pause 10 999",
        ):
            with self.subTest(invalid=invalid), self.assertRaises(hub.HubError):
                hub.control(invalid, self.state_path, "owner-1")

    def test_concurrent_actions_leave_one_valid_atomic_revision(self):
        commands = [action for _ in range(8) for action in ("pause", "resume", "stop", "resume")]

        def mutate(action):
            for _attempt in range(100):
                listed = self.response("v1 list")
                try:
                    return self.response(f"v1 {action} {listed['revision']} 0")
                except hub.HubError:
                    continue
            self.fail("concurrent mutation did not converge")

        with ThreadPoolExecutor(max_workers=8) as pool:
            responses = list(pool.map(mutate, commands))
        state = json.loads(self.state_path.read_text(encoding="utf-8"))
        self.assertEqual(hub._validate_state(state), state)
        self.assertTrue(all(response["ok"] for response in responses))
        self.assertGreater(state["revision"], 7)

    def test_resume_moves_stopped_tunnel_back_to_running_desired_state(self):
        stopped = self.response("v1 stop 7 0")
        self.assertEqual((stopped["revision"], stopped["tunnels"][0]["state"]), (8, "stopped"))
        resumed = self.response("v1 resume 8 0")
        self.assertEqual((resumed["revision"], resumed["tunnels"][0]["state"]), (9, "running"))

    def test_gate_observes_changes_immediately_and_fails_closed(self):
        server = hub.GateServer(("127.0.0.1", 0), self.state_path)
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        try:
            def status():
                connection = http.client.HTTPConnection(*server.server_address, timeout=2)
                connection.request("GET", f"/authorize/{self.SELF_ID}")
                response = connection.getresponse()
                result = (response.status, response.read())
                connection.close()
                return result

            self.assertEqual(status(), (204, b""))
            self.response("v1 pause 7 0")
            self.assertEqual(status(), (503, hub.UNAVAILABLE))
            self.response("v1 resume 8 0")
            self.assertEqual(status(), (204, b""))
            self.state_path.write_text("not json", encoding="utf-8")
            self.assertEqual(status(), (503, hub.UNAVAILABLE))
            self.state_path.write_text(
                '{"v":2,"revision":1,"tunnels":'
                '[{"id":"tunnel_a1b2c3d4","owner":"owner-1","state":[]}]}'
            )
            self.assertEqual(status(), (503, hub.UNAVAILABLE))
        finally:
            server.shutdown()
            server.server_close()
            worker.join(timeout=2)

    def test_gate_headers_do_not_fingerprint_the_control_service(self):
        server = hub.GateServer(("127.0.0.1", 0), self.state_path)
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        try:
            connection = http.client.HTTPConnection(*server.server_address, timeout=2)
            connection.request("GET", f"/authorize/{self.SELF_ID}")
            response = connection.getresponse()
            headers = {name.casefold(): value for name, value in response.getheaders()}
            response.read()
            connection.close()
            self.assertEqual(response.status, 204)
            self.assertEqual(headers["cache-control"], "no-store")
            self.assertEqual(headers["content-type"], "application/json")
            self.assertNotIn("server", headers)
            self.assertNotIn("date", headers)
        finally:
            server.shutdown()
            server.server_close()
            worker.join(timeout=2)

    def test_forced_command_always_emits_one_bounded_generic_failure_line(self):
        self.assertLessEqual(len(hub.FAILURE), 64 << 10)
        self.assertEqual(hub.FAILURE.count(b"\n"), 1)
        self.assertEqual(
            json.loads(hub.FAILURE),
            {"v": 1, "ok": False, "error": "request_failed"},
        )
        raw = hub.control("v1 list", self.state_path, "owner-1")
        self.assertLessEqual(len(raw), 64 << 10)
        self.assertEqual(raw.count(b"\n"), 1)
        self.assertNotIn(self.SELF_ID.encode(), raw)
        self.assertNotIn(b"owner-1", raw)

        environment = os.environ.copy()
        environment["SSH_ORIGINAL_COMMAND"] = "v1 list trailing"
        result = subprocess.run(
            [
                sys.executable,
                str(MODULE_PATH),
                "--state",
                str(self.state_path),
                "control",
                "--owner",
                "owner-1",
            ],
            capture_output=True,
            check=False,
            env=environment,
            timeout=5,
        )
        self.assertEqual((result.returncode, result.stdout, result.stderr), (1, hub.FAILURE, b""))

    def test_legacy_state_migrates_without_exposing_or_guessing_owner(self):
        self.state_path.write_text(
            json.dumps(
                {
                    "v": 1,
                    "revision": 3,
                    "tunnels": [
                        {"id": self.SELF_ID, "name": "Tunnel 1", "state": "running"}
                    ],
                }
            ),
            encoding="utf-8",
        )

        listed = self.response("v1 list")
        self.assertEqual(
            listed["tunnels"], [{"name": "Tunnel 1", "state": "running"}]
        )
        migrated = json.loads(self.state_path.read_text(encoding="utf-8"))
        self.assertEqual(
            migrated,
            {
                "v": 2,
                "revision": 3,
                "tunnels": [
                    {"id": self.SELF_ID, "owner": None, "state": "running"}
                ],
            },
        )
        serialized = json.dumps(listed).lower()
        self.assertNotIn(self.SELF_ID, serialized)
        self.assertNotIn("owner", serialized)

        invalid = {
            "v": 2,
            "revision": 3,
            "tunnels": [{"id": self.SELF_ID, "state": "running"}],
        }
        with self.assertRaises(hub.HubError):
            hub._validate_state(invalid)

    def test_deployment_templates_keep_control_separate_and_gate_before_proxy(self):
        caddy = (ROOT / "deploy" / "Caddyfile.tunnel-hub.example").read_text()
        self.assertLess(caddy.index("route {"), caddy.index("header {"))
        self.assertLess(caddy.index("header {"), caddy.index("forward_auth"))
        self.assertLess(caddy.index("forward_auth"), caddy.index("reverse_proxy"))
        self.assertIn(
            "header_up X-Tunnel-Client-IP {http.request.remote.host}", caddy
        )
        self.assertIn("-Server", caddy)
        self.assertIn("-Date", caddy)
        self.assertIn('Cache-Control "no-store"', caddy)
        self.assertIn('Strict-Transport-Security "max-age=31536000"', caddy)
        self.assertIn("handle_errors {", caddy)
        self.assertIn("http.request.orig_uri.path", caddy)
        self.assertIn('"type":"tunnel_error"', caddy)
        sshd = (ROOT / "deploy" / "sshd_config.tunnel-control").read_text()
        self.assertIn("DisableForwarding yes", sshd)
        keys = (ROOT / "deploy" / "authorized_keys.tunnel-control.example").read_text()
        self.assertEqual(keys.count("restrict,command="), 2)
        self.assertNotIn("model-tunnel_ed25519", keys)


if __name__ == "__main__":
    unittest.main()

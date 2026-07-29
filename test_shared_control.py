import json
import subprocess
import unittest
from types import SimpleNamespace
from unittest.mock import patch

import relay_tunnel
import relay_http
from relay_ui import RelayApp
from textual.widgets import Button, DataTable


def response(state="running", revision=3):
    return (
        json.dumps(
            {
                "v": 1,
                "ok": True,
                "revision": revision,
                "tunnels": [
                    {"name": "Ваш коннект", "state": state}
                ],
            },
            separators=(",", ":"),
        ).encode()
        + b"\n"
    )


class SharedTunnelControlTest(unittest.TestCase):
    def test_parser_accepts_only_provider_neutral_schema(self):
        parsed = relay_tunnel._parse_control_snapshot(response())
        self.assertEqual(
            parsed,
            {
                "available": True,
                "revision": 3,
                "tunnels": [
                    {
                        "position": 0,
                        "name": "Ваш коннект",
                        "state": "running",
                    }
                ],
                "error": "",
            },
        )
        leaked = json.loads(response())
        leaked["tunnels"][0]["provider"] = "hidden"
        with self.assertRaises(ValueError):
            relay_tunnel._parse_control_snapshot(
                json.dumps(leaked, separators=(",", ":")).encode() + b"\n"
            )
        for malformed in (b"", response() + b"{}\n", response()[:-1], b"[]\n"):
            with self.subTest(malformed=malformed), self.assertRaises(
                (relay_tunnel.InvalidJson, ValueError)
            ):
                relay_tunnel._parse_control_snapshot(malformed)

    def test_ssh_control_is_pinned_isolated_and_exact(self):
        completed = SimpleNamespace(returncode=0, stdout=response("paused", 4))
        with (
            patch.object(relay_tunnel, "_find_ssh", return_value="ssh.exe"),
            patch.object(
                relay_tunnel,
                "_find_control_identity",
                return_value="control-key",
            ),
            patch.object(
                relay_tunnel,
                "_ensure_ssh_known_hosts",
                return_value="known-hosts",
            ),
            patch.object(relay_tunnel.subprocess, "run", return_value=completed) as run,
        ):
            result = relay_tunnel.SharedTunnelControl().control(0, 3, "pause")
        self.assertEqual(result["tunnels"][0]["state"], "paused")
        command = run.call_args.args[0]
        self.assertEqual(
            command[-5:],
            [relay_tunnel.CONTROL_SSH_DESTINATION, "v1", "pause", "3", "0"],
        )
        self.assertIn("NUL", command)
        self.assertIn("ClearAllForwardings=yes", command)
        self.assertIn("StrictHostKeyChecking=yes", command)
        self.assertNotIn("FREEMODEL_API_KEY", run.call_args.kwargs["env"])
        self.assertIs(run.call_args.kwargs["stdin"], subprocess.DEVNULL)
        self.assertIs(run.call_args.kwargs["stderr"], subprocess.DEVNULL)
        self.assertFalse(run.call_args.kwargs["shell"])

    def test_missing_key_fails_closed_without_breaking_ui_poll(self):
        with patch.object(relay_tunnel, "_find_control_identity", return_value=None):
            control = relay_tunnel.SharedTunnelControl()
            self.assertEqual(control.snapshot()["available"], False)
            with self.assertRaises(RuntimeError):
                control.control(0, 3, "stop")
        with self.assertRaises(ValueError):
            relay_tunnel.SharedTunnelControl().control(-1, 3, "stop")


class SharedTunnelUITest(unittest.IsolatedAsyncioTestCase):
    async def test_shared_tab_syncs_controls_and_stays_usable_when_compact(self):
        server = relay_http.RelayServer(
            ("127.0.0.1", 0),
            relay_http.RelayHandler,
            echo_api_key="",
            config=False,
            history=False,
        )
        state = {"revision": 1, "value": "running"}

        def snapshot():
            return {
                "available": True,
                "revision": state["revision"],
                "tunnels": [
                    {
                        "position": 0,
                        "name": "Ваш коннект",
                        "state": state["value"],
                    }
                ],
                "error": "",
            }

        def control(position, revision, action):
            self.assertEqual((position, revision), (0, state["revision"]))
            state["value"] = {"pause": "paused", "resume": "running", "stop": "stopped"}[action]
            state["revision"] += 1
            return snapshot()

        server._shared_tunnel_control = SimpleNamespace(
            snapshot=snapshot,
            control=control,
        )
        try:
            for size in ((80, 24), (120, 35)):
                app = RelayApp(server)
                async with app.run_test(size=size) as pilot:
                    app.query_one("#main-tabs").active = "shared-tunnels"
                    for _ in range(20):
                        await pilot.pause(0.05)
                        if app.query_one("#shared-tunnels-table", DataTable).row_count:
                            break
                    table = app.query_one("#shared-tunnels-table", DataTable)
                    self.assertEqual(table.row_count, 1)
                    for selector in (
                        "#shared-tunnel-refresh",
                        "#shared-tunnel-pause",
                        "#shared-tunnel-resume",
                        "#shared-tunnel-stop",
                    ):
                        self.assertGreater(app.query_one(selector, Button).region.width, 0)
                    screenshot = app.export_screenshot()
                    self.assertEqual(table.get_row_at(0)[1].plain.strip(), "Ваш коннект")
                    self.assertNotIn("tunnel_a1b2c3d4", screenshot)
                    self.assertNotIn("api.echogate.one", screenshot.casefold())
                    if size == (80, 24):
                        await pilot.click("#shared-tunnel-pause")
                        for _ in range(20):
                            await pilot.pause(0.05)
                            if state["value"] == "paused" and not app._shared_tunnel_busy:
                                break
                        self.assertEqual(state["value"], "paused")
                        self.assertFalse(
                            app.query_one("#shared-tunnel-resume", Button).disabled
                        )
        finally:
            server.server_close()


if __name__ == "__main__":
    unittest.main()

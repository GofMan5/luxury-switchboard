import threading
import unittest
from unittest.mock import patch

from rich.text import Text
from textual.widgets import Button, DataTable, Select, SelectionList, Static

from relay_ui import (
    ActivityTable,
    PALETTE,
    ProviderFormScreen,
    RelayApp,
    TunnelClientsTable,
    _operation_error,
    _tunnel_error,
)


class _UIServer:
    server_address = ("127.0.0.1", 8798)

    def __init__(self) -> None:
        self.periods: list[str] = []
        self.metrics = {
            "providers": [],
            "live": [],
            "recent": [],
            "actual_rpm": 0,
            "rpm": 0,
        }

    def snapshot(self):
        return self.metrics

    def providers(self):
        return ()

    def tunnel_snapshot(self):
        return {
            "state": "stopped",
            "url": "",
            "allowed_count": 0,
            "error": "",
            "route_available": True,
        }

    def shared_tunnels(self):
        return {"available": False, "revision": 0, "tunnels": []}

    def history_stats(self, period):
        self.periods.append(period)
        return {
            "requests": 4,
            "successes": 3,
            "errors": 1,
            "cancelled": 0,
            "average_ms": 125,
            "p95_ms": 250,
            "total_tokens": 500,
            "context_tokens": 300,
            "output_tokens": 200,
            "cached_tokens": 100,
            "reasoning_tokens": 50,
            "retries": 2,
            "retries_429": 1,
        }

    def history_breakdown(self, _period):
        return [
            {
                "provider": "Provider",
                "model": "model",
                "requests": 4,
                "total_tokens": 500,
                "cached_tokens": 100,
                "average_ms": 125,
            }
        ]


class _KeyUIServer(_UIServer):
    def __init__(self) -> None:
        super().__init__()
        self.keys = [
            {
                "id": "env",
                "label": "Env Lite",
                "rpm": 30,
                "pinned": True,
                "cooldown_ms": 5000,
                "blocked_models": 1,
            },
            {"id": "key-a", "label": "Key 2", "rpm": 60, "pinned": False},
            {"id": "key-b", "label": "Key 3", "rpm": 120, "pinned": False},
        ]
        self.moves: list[tuple[str, str, int]] = []
        self.resets: list[tuple[str, str]] = []

    def providers(self):
        return (
            {
                "id": "echo",
                "name": "EchoGate",
                "upstream": "https://example.invalid/v1",
                "auth_mode": "bearer",
                "rpm": 0,
                "effective_rpm": 210,
                "actual_rpm": 0,
                "queued": 0,
                "key_count": len(self.keys),
                "cache_1h": True,
                "active": True,
            },
        )

    def provider_keys(self, provider_id):
        assert provider_id == "echo"
        last = len(self.keys) - 1
        return tuple(
            {
                **row,
                "proxy": "Direct",
                "actual_rpm": 0,
                "cooldown_ms": row.get("cooldown_ms", 0),
                "blocked_models": row.get("blocked_models", 0),
                "retries_429": 0,
                "can_move_up": not row["pinned"] and index > 1,
                "can_move_down": not row["pinned"] and index < last,
            }
            for index, row in enumerate(self.keys)
        )

    def move_provider_key(self, provider_id, fingerprint, direction):
        index = next(i for i, row in enumerate(self.keys) if row["id"] == fingerprint)
        target = index + direction
        if self.keys[index]["pinned"] or target <= 0 or target >= len(self.keys):
            raise ValueError("Key priority is fixed")
        self.keys[index], self.keys[target] = self.keys[target], self.keys[index]
        self.moves.append((provider_id, fingerprint, direction))

    def reset_provider_key_cooldown(self, provider_id, fingerprint):
        row = next(row for row in self.keys if row["id"] == fingerprint)
        row["cooldown_ms"] = 0
        row["blocked_models"] = 0
        self.resets.append((provider_id, fingerprint))


class _TunnelUIServer(_UIServer):
    def __init__(self) -> None:
        super().__init__()
        self.provider_id = "echo"
        self.provider_changes: list[str] = []
        self.allowed = ("model-a",)
        self.rpm = 30
        self.state = "stopped"
        self.tunnel_url = ""
        self.shared_available = True
        self.shared_state = "running"
        self.shared_revision = 1
        self.shared_block = False
        self.shared_fetch_started = threading.Event()
        self.shared_fetch_release = threading.Event()
        self.block_fetch_provider = ""
        self.fetch_calls: list[str] = []
        self.fetch_started = threading.Event()
        self.fetch_release = threading.Event()
        self.allowed_writes: list[tuple[str, tuple[str, ...]]] = []
        self.probe_calls: list[str] = []
        self.probe_states: dict[str, str] = {}
        self.probe_results: dict[str, str] = {}
        self.probe_block = False
        self.probe_started = threading.Event()
        self.probe_release = threading.Event()
        self.probe_active = 0
        self.probe_max_active = 0
        self.probe_lock = threading.Lock()
        self.catalogs = {
            "echo": ("model-a", "model-b"),
            "local": ("local-a", "local-b", "local-c"),
        }
        self.live_events = [self._event(1, state="active", status=200)]
        self.recent_events = []

    @staticmethod
    def _event(sequence, **values):
        return {
            "id": sequence,
            "ip": "203.0.113.7",
            "method": "POST",
            "path": "/v1/responses?private=query",
            "model": "model-a",
            "status": 200,
            "state": "success",
            "latency_ms": 125,
            "request_bytes": 80,
            "response_bytes": 160,
            "provider": "FORBIDDEN_PROVIDER",
            "api_key": "FORBIDDEN_KEY",
            "body": "FORBIDDEN_BODY",
            "prompt": "FORBIDDEN_PROMPT",
            **values,
        }

    def providers(self):
        return tuple(
            {
                "id": provider_id,
                "name": name,
                "upstream": f"https://{provider_id}.invalid/v1",
                "auth_mode": "bearer",
                "rpm": 0,
                "actual_rpm": 0,
                "queued": 0,
                "key_count": 1,
                "cache_1h": False,
                "active": provider_id == "local",
            }
            for provider_id, name in (("local", "Local"), ("echo", "Echo"))
        )

    def tunnel_snapshot(self):
        return {
            "state": self.state,
            "url": self.tunnel_url,
            "allowed_count": len(self.allowed),
            "error": "",
            "route_available": True,
            "clients": (
                {
                    "ip": "203.0.113.7",
                    "first_seen_ms": 1_700_000_000_000,
                    "idle_ms": 750,
                    "connected": 3,
                    "actual_rpm": 17,
                    "active": 1,
                    "provider": "FORBIDDEN_PROVIDER",
                },
            ),
            "live": tuple(self.live_events),
            "recent": tuple(self.recent_events),
        }

    def tunnel_provider_id(self):
        return self.provider_id

    def set_tunnel_provider(self, provider_id):
        if self.state == "running":
            raise ValueError("Stop tunnel before changing provider")
        if provider_id not in self.catalogs:
            raise ValueError("Unknown provider")
        self.provider_id = provider_id
        self.allowed = ()
        with self.probe_lock:
            self.probe_states.clear()
        self.provider_changes.append(provider_id)
        return provider_id

    def fetch_tunnel_models(self):
        provider_id = self.provider_id
        self.fetch_calls.append(provider_id)
        if provider_id == self.block_fetch_provider:
            self.fetch_started.set()
            self.fetch_release.wait(5)
        return {"data": [{"id": model} for model in self.catalogs[provider_id]]}

    def tunnel_allowed_models(self):
        return self.allowed

    def set_tunnel_allowed_models(self, models):
        self.allowed = tuple(models)
        self.allowed_writes.append((self.provider_id, self.allowed))
        return self.allowed

    def _shared_snapshot(self):
        return {
            "available": self.shared_available,
            "revision": self.shared_revision,
            "tunnels": (
                {"position": 0, "name": "Ваш коннект", "state": self.shared_state},
            ) if self.shared_available else (),
        }

    def shared_tunnels(self):
        snapshot = self._shared_snapshot()
        if self.shared_block:
            self.shared_fetch_started.set()
            self.shared_fetch_release.wait(5)
        return snapshot

    def control_shared_tunnel(self, position, revision, action):
        if position != 0 or revision != self.shared_revision:
            raise ValueError("Stale shared tunnel")
        if action == "pause" and self.shared_state == "running":
            self.shared_state = "paused"
        elif action == "resume" and self.shared_state in {"paused", "stopped"}:
            self.shared_state = "running"
        elif action == "stop" and self.shared_state in {"running", "paused"}:
            self.shared_state = "stopped"
        else:
            raise ValueError("Invalid shared tunnel operation")
        self.shared_revision += 1
        return self._shared_snapshot()

    def tunnel_model_probes(self):
        with self.probe_lock:
            return tuple(
                {"model": model, "state": state}
                for model in self.catalogs[self.provider_id]
                if (state := self.probe_states.get(model)) is not None
            )

    def probe_tunnel_model(self, model):
        provider_id = self.provider_id
        if model not in self.catalogs[provider_id]:
            raise ValueError("Unknown model")
        with self.probe_lock:
            self.probe_calls.append(model)
            self.probe_states[model] = "testing"
            self.probe_active += 1
            self.probe_max_active = max(self.probe_max_active, self.probe_active)
        try:
            if self.probe_block:
                self.probe_started.set()
                self.probe_release.wait(5)
            state = self.probe_results.get(model, "available")
        finally:
            with self.probe_lock:
                self.probe_active -= 1
        with self.probe_lock:
            if self.provider_id == provider_id:
                self.probe_states[model] = state
        return {"model": model, "state": state}

    def tunnel_rpm_per_ip(self):
        return self.rpm

    def set_tunnel_rpm_per_ip(self, rpm):
        self.rpm = rpm
        return rpm

    def tunnel_publisher_profile(self):
        return ""


class RelayUILayoutTest(unittest.IsolatedAsyncioTestCase):
    def test_tunnel_error_only_allows_curated_messages(self):
        message = "Tunnel publisher profile is already active"
        self.assertEqual(_tunnel_error(message), message)
        self.assertEqual(_tunnel_error("secret upstream failure"), "Tunnel operation failed")
        self.assertEqual(
            _tunnel_error("secret upstream failure", "connection unavailable"),
            "connection unavailable",
        )
        self.assertEqual(
            _tunnel_error(
                "", _operation_error(ValueError("Select at least one tunnel model"))
            ),
            "Select at least one tunnel model",
        )

    async def test_provider_authentication_is_visible_and_keyboard_selectable(self):
        app = RelayApp(_UIServer())
        saved = []
        async with app.run_test(size=(80, 30)) as pilot:
            form = ProviderFormScreen(
                {
                    "name": "Provider",
                    "upstream": "https://example.invalid/v1",
                    "auth_mode": "bearer",
                    "rpm": 0,
                    "cache_1h": False,
                }
            )
            app.push_screen(form, saved.append)
            await pilot.pause()
            buttons = [
                form.query_one(f"#provider-auth-{value}", Button)
                for _label, value in (
                    ("Automatic", "auto"),
                    ("Pass through", "passthrough"),
                    ("Bearer", "bearer"),
                    ("x-api-key", "x-api-key"),
                )
            ]
            self.assertTrue(all(button.region.width > 0 for button in buttons))
            self.assertTrue(buttons[2].has_class("provider-auth-selected"))
            buttons[3].focus()
            await pilot.press("enter")
            self.assertEqual(form.auth_mode, "x-api-key")
            self.assertTrue(buttons[3].has_class("provider-auth-selected"))
            form.query_one("#provider-form-dialog").scroll_end(animate=False)
            await pilot.pause()
            await pilot.click("#provider-save")
            await pilot.pause()
            self.assertEqual(saved[0]["auth_mode"], "x-api-key")

    async def test_key_priority_controls_move_rows_and_fit_supported_sizes(self):
        for size, compact in (
            ((60, 20), True),
            ((80, 24), True),
            ((120, 35), False),
        ):
            with self.subTest(size=size):
                server = _KeyUIServer()
                app = RelayApp(server)
                async with app.run_test(size=size) as pilot:
                    app.query_one("#main-tabs").active = "providers"
                    await pilot.pause(0.35)
                    app.query_one("#providers-scroll").scroll_end(animate=False)
                    await pilot.pause()

                    self.assertEqual(app.screen.has_class("compact"), compact)
                    editor = app.query_one("#key-editor")
                    controls = [
                        app.query_one(control)
                        for control in (
                            "#key-input",
                            "#key-rpm-input",
                            "#key-proxy-input",
                            "#add-key",
                            "#update-key-settings",
                            "#remove-key",
                            "#move-key-up",
                            "#move-key-down",
                            "#reset-key-cooldown",
                        )
                    ]
                    self.assertTrue(
                        all(
                            control.region.width > 0
                            and editor.region.x <= control.region.x
                            and control.region.right <= editor.region.right
                            for control in controls
                        )
                    )
                    inputs = controls[:3]
                    actions = controls[3:]
                    if compact:
                        self.assertEqual(len({control.region.y for control in inputs}), 1)
                        self.assertEqual(len({control.region.y for control in actions}), 1)
                        self.assertGreater(actions[0].region.y, inputs[0].region.y)
                    else:
                        self.assertEqual(len({control.region.y for control in controls}), 1)

                    if size != (120, 35):
                        continue
                    table = app.query_one("#keys-table", DataTable)
                    table.move_cursor(row=0, column=0, animate=False)
                    await pilot.pause()
                    for button_id in (
                        "#update-key-settings",
                        "#remove-key",
                        "#move-key-up",
                        "#move-key-down",
                    ):
                        self.assertTrue(app.query_one(button_id, Button).disabled)
                    self.assertFalse(
                        app.query_one("#reset-key-cooldown", Button).disabled
                    )
                    await pilot.click("#reset-key-cooldown")
                    await pilot.pause()
                    self.assertEqual(server.resets, [("echo", "env")])

                    table.move_cursor(row=1, column=0, animate=False)
                    await pilot.pause()
                    self.assertTrue(app.query_one("#move-key-up", Button).disabled)
                    self.assertFalse(app.query_one("#move-key-down", Button).disabled)
                    await pilot.click("#move-key-down")
                    await pilot.pause()
                    self.assertEqual(app._key_order, ("env", "key-b", "key-a"))
                    self.assertEqual(server.moves, [("echo", "key-a", 1)])
                    self.assertFalse(app.query_one("#move-key-up", Button).disabled)
                    self.assertTrue(app.query_one("#move-key-down", Button).disabled)

                    await pilot.click("#move-key-up")
                    await pilot.pause()
                    self.assertEqual(app._key_order, ("env", "key-a", "key-b"))
                    self.assertEqual(server.moves[-1], ("echo", "key-a", -1))
                    self.assertTrue(app.query_one("#move-key-up", Button).disabled)
                    self.assertFalse(app.query_one("#move-key-down", Button).disabled)

    async def test_stats_period_controls_are_clear_adaptive_and_keyboard_usable(self):
        for size, compact in (
            ((60, 20), True),
            ((80, 24), True),
            ((120, 35), False),
        ):
            with self.subTest(size=size):
                server = _UIServer()
                app = RelayApp(server)
                async with app.run_test(size=size) as pilot:
                    app.query_one("#main-tabs").active = "stats"
                    for _ in range(20):
                        await pilot.pause(0.05)
                        if server.periods:
                            break

                    self.assertEqual(app.screen.has_class("compact"), compact)
                    self.assertEqual(len(app.query("#stats-period")), 0)
                    controls = app.query_one("#stats-controls")
                    buttons = [
                        app.query_one(f"#stats-period-{period}", Button)
                        for period in ("24h", "48h", "72h", "all")
                    ]
                    buttons.append(app.query_one("#refresh-stats", Button))
                    self.assertEqual(len({button.region.y for button in buttons}), 1)
                    self.assertTrue(
                        all(
                            button.region.width > 0
                            and controls.region.x <= button.region.x
                            and button.region.right <= controls.region.right
                            for button in buttons
                        )
                    )
                    self.assertGreater(app.query_one("#stats-status").region.width, 0)
                    breakdown = app.query_one("#stats-breakdown", DataTable)
                    self.assertGreaterEqual(breakdown.region.height, 5 if compact else 8)
                    stats_scroll = app.query_one("#stats-scroll")
                    if size == (60, 20):
                        self.assertGreater(stats_scroll.max_scroll_y, 0)
                    else:
                        self.assertLessEqual(
                            breakdown.region.bottom, app.screen.region.bottom
                        )

                    await pilot.click("#stats-period-48h")
                    for _ in range(20):
                        await pilot.pause(0.05)
                        if server.periods and server.periods[-1] == "48h":
                            break
                    self.assertEqual(server.periods[-1], "48h")
                    self.assertTrue(
                        app.query_one("#stats-period-48h", Button).has_class(
                            "stats-period-selected"
                        )
                    )

                    app.query_one("#stats-period-72h", Button).focus()
                    await pilot.press("enter")
                    for _ in range(20):
                        await pilot.pause(0.05)
                        if server.periods and server.periods[-1] == "72h":
                            break
                    self.assertEqual(server.periods[-1], "72h")
                    screenshot = app.export_screenshot()
                    for label in ("24h", "48h", "72h", "All", "Refresh"):
                        self.assertIn(label, screenshot)
                    self.assertNotIn("24 hours", screenshot)
                    if size == (60, 20):
                        stats_scroll.scroll_end(animate=False)
                        await pilot.pause()
                        self.assertGreater(breakdown.region.width, 0)
                        self.assertLess(breakdown.region.y, app.screen.region.bottom)

    async def test_live_row_updates_final_status_state_detail_and_colors(self):
        server = _UIServer()
        live = {
            "_request_id": 7,
            "time": "12:00:00",
            "provider": "Provider",
            "provider_id": "provider",
            "model": "model",
            "method": "POST",
            "path": "/v1/responses",
            "state": "queued",
            "status": None,
        }
        server.metrics["live"] = [live]
        app = RelayApp(server)
        async with app.run_test(size=(120, 35)) as pilot:
            await pilot.pause(0.35)
            server.metrics["history_error"] = "History write failed"
            app._refresh_live()
            self.assertIn(
                "history not recording",
                str(app.query_one("#active-provider", Static).content),
            )
            table = app.query_one("#activity", ActivityTable)
            self.assertEqual(table.row_count, 1)
            self.assertEqual(table.get_cell("request:7", "state").plain, "● Queued")
            self.assertEqual(table.get_cell("request:7", "status").plain, "—")

            finished = dict(live, state="error", status=504, latency_ms=900)
            server.metrics["live"] = []
            server.metrics["recent"] = [finished]
            await pilot.pause(0.35)
            self.assertEqual(table.row_count, 1)
            self.assertEqual(table.get_cell("request:7", "state").plain, "● Error")
            self.assertEqual(
                str(table.get_cell("request:7", "state").style), PALETTE["error"]
            )
            self.assertEqual(
                table.get_cell("request:7", "status").plain,
                "504 Gateway Timeout",
            )
            self.assertEqual(
                str(table.get_cell("request:7", "status").style), PALETTE["error"]
            )
            self.assertEqual(
                table._details["request:7"]["Status"], "504 Gateway Timeout"
            )
            self.assertEqual(table._details["request:7"]["State"], "error")

            table.move_cursor(row=0, column=0, animate=False)
            await pilot.click("#activity", times=2)
            await pilot.pause()
            detail = app.screen.query_one("#event-detail", Static).content
            self.assertIsInstance(detail, Text)
            self.assertIn("Status           504 Gateway Timeout", detail.plain)
            self.assertIn("State            Error", detail.plain)
            self.assertEqual(
                self._style_at(detail, "504 Gateway Timeout"), PALETTE["error"]
            )
            self.assertEqual(self._style_at(detail, "Error"), PALETTE["error"])
            await pilot.click("#detail-close")
            await pilot.pause()

            server.metrics["recent"] = [dict(finished, state="ok", status=200)]
            await pilot.pause(0.35)
            self.assertEqual(table.row_count, 1)
            self.assertEqual(table.get_cell("request:7", "state").plain, "● Done")
            self.assertEqual(table.get_cell("request:7", "status").plain, "200 OK")
            table.focus()
            await pilot.press("enter")
            await pilot.pause()
            detail = app.screen.query_one("#event-detail", Static).content
            self.assertIn("Status           200 OK", detail.plain)
            self.assertIn("State            Done", detail.plain)
            self.assertEqual(self._style_at(detail, "200 OK"), PALETTE["success"])
            self.assertEqual(self._style_at(detail, "Done"), PALETTE["success"])

    async def test_tunnel_clients_keyboard_modal_is_bounded_safe_and_responsive(self):
        server = _TunnelUIServer()
        app = RelayApp(server)
        async with app.run_test(size=(80, 24)) as pilot:
            app.query_one("#main-tabs").active = "tunnel"
            await pilot.pause(0.65)
            table = app.query_one("#tunnel-clients", TunnelClientsTable)
            self.assertEqual(table.row_count, 1)
            self.assertEqual(table.get_cell_at((0, 0)).plain, "203.0.113.7")
            self.assertEqual(table.get_cell_at((0, 1)).plain, "17")
            self.assertEqual(table.get_cell_at((0, 2)).plain, "3")
            self.assertEqual(table.get_cell_at((0, 3)).plain, "1")
            self.assertEqual(table.get_cell_at((0, 4)).plain, "2")

            table.scroll_visible(animate=False)
            table.focus()
            await pilot.press("enter")
            await pilot.pause()
            dialog = app.screen.query_one("#tunnel-client-dialog")
            self.assertGreater(dialog.region.width, 0)
            self.assertGreater(dialog.region.height, 0)
            self.assertGreaterEqual(dialog.region.x, app.screen.region.x)
            self.assertGreaterEqual(dialog.region.y, app.screen.region.y)
            self.assertLessEqual(dialog.region.right, app.screen.region.right)
            self.assertLessEqual(dialog.region.bottom, app.screen.region.bottom)

            events = app.screen.query_one("#tunnel-client-events", DataTable)
            self.assertEqual(events.row_count, 1)
            self.assertIn(
                "2 queued",
                str(app.screen.query_one("#tunnel-client-summary", Static).content),
            )
            safe_text = self._table_text(events)
            for expected in ("POST", "/v1/responses", "model-a", "200 OK"):
                self.assertIn(expected, safe_text)
            self.assertNotIn("private=query", safe_text)
            for forbidden in (
                "FORBIDDEN_PROVIDER",
                "FORBIDDEN_KEY",
                "FORBIDDEN_BODY",
                "FORBIDDEN_PROMPT",
                "provider",
                "api_key",
                "body",
                "prompt",
            ):
                self.assertNotIn(forbidden, safe_text)

            server.live_events = []
            server.recent_events = [
                server._event(index, path=f"/v1/next/{index}", status=201)
                for index in range(100)
            ]
            await pilot.pause(0.65)
            self.assertEqual(events.row_count, 80)
            self.assertIn("/v1/next/99", self._table_text(events))

    async def test_tunnel_clients_mouse_provider_and_all_models_at_wide_size(self):
        server = _TunnelUIServer()
        server.catalogs["echo"] = tuple(f"model-{index}" for index in range(128))
        server.allowed = (server.catalogs["echo"][0],)
        app = RelayApp(server)
        async with app.run_test(size=(180, 40)) as pilot:
            app.query_one("#main-tabs").active = "tunnel"
            for _ in range(40):
                await pilot.pause(0.05)
                if app._tunnel_models_loaded:
                    break

            provider = app.query_one("#tunnel-provider-select", Select)
            self.assertEqual(provider.value, "echo")
            self.assertFalse(provider.disabled)
            previous_generation = app._tunnel_models_generation
            server.allowed_writes.clear()
            fetch_count = len(server.fetch_calls)
            server.block_fetch_provider = "echo"
            server.fetch_started.clear()
            await pilot.click("#tunnel-all-models")
            await pilot.pause(0.1)
            self.assertFalse(server.fetch_started.is_set())
            self.assertEqual(len(server.fetch_calls), fetch_count)
            self.assertFalse(app._tunnel_models_loading)
            self.assertEqual(server.allowed, server.catalogs["echo"])
            self.assertEqual(
                server.allowed_writes,
                [("echo", server.catalogs["echo"])],
            )

            selected_generation = app._tunnel_models_generation
            await pilot.click("#tunnel-all-models")
            await pilot.pause(0.2)
            self.assertFalse(server.fetch_started.is_set())
            self.assertEqual(len(server.fetch_calls), fetch_count)
            self.assertEqual(app._tunnel_models_generation, selected_generation)
            self.assertFalse(app._tunnel_models_loading)
            self.assertEqual(server.allowed, ())
            self.assertEqual(
                server.allowed_writes,
                [("echo", server.catalogs["echo"]), ("echo", ())],
            )
            self.assertNotIn(
                "✓", str(app.query_one("#tunnel-all-models", Button).label)
            )
            self.assertEqual(
                tuple(app.query_one("#tunnel-models", SelectionList).selected), ()
            )

            app._load_tunnel_models()
            for _ in range(20):
                await pilot.pause(0.05)
                if server.fetch_started.is_set():
                    break
            self.assertTrue(server.fetch_started.is_set())
            try:
                provider.value = "local"
                for _ in range(20):
                    await pilot.pause(0.05)
                    if server.provider_changes:
                        break
            finally:
                server.fetch_release.set()
            await pilot.pause(0.65)
            self.assertEqual(server.provider_changes, ["local"])
            self.assertEqual(server.provider_id, "local")
            self.assertGreater(app._tunnel_models_generation, previous_generation)
            self.assertEqual(set(app._tunnel_models), set(server.catalogs["local"]))
            self.assertNotIn(
                ("local", server.catalogs["echo"]), server.allowed_writes
            )

            local_fetch_count = len(server.fetch_calls)
            await pilot.click("#tunnel-all-models")
            await pilot.pause(0.1)
            self.assertEqual(len(server.fetch_calls), local_fetch_count)
            self.assertEqual(server.allowed, server.catalogs["local"])
            self.assertIn("✓", str(app.query_one("#tunnel-all-models", Button).label))
            models = app.query_one("#tunnel-models", SelectionList)
            self.assertEqual(set(models.selected), set(server.catalogs["local"]))

            table = app.query_one("#tunnel-clients", TunnelClientsTable)
            table.scroll_visible(animate=False)
            table.move_cursor(row=0, column=0, animate=False)
            await pilot.click("#tunnel-clients", times=2)
            await pilot.pause()
            dialog = app.screen.query_one("#tunnel-client-dialog")
            self.assertLessEqual(dialog.region.right, app.screen.region.right)
            self.assertLessEqual(dialog.region.bottom, app.screen.region.bottom)

            await pilot.click("#tunnel-client-close")
            server.state = "running"
            app._refresh_tunnel()
            self.assertTrue(provider.disabled)

    async def test_tunnel_provider_model_controls_fit_supported_sizes(self):
        for size, layout in (
            ((60, 24), "tiny"),
            ((100, 35), "narrow"),
            ((180, 50), "wide"),
        ):
            with self.subTest(size=size):
                server = _TunnelUIServer()
                long_model = "provider/" + "x" * 180 + "-TAILMARKER"
                server.catalogs["echo"] = (long_model, "model-b")
                server.allowed = (long_model,)
                server.probe_states = {long_model: "available"}
                app = RelayApp(server)
                async with app.run_test(size=size) as pilot:
                    app.query_one("#main-tabs").active = "tunnel"
                    for _ in range(40):
                        await pilot.pause(0.05)
                        if app._tunnel_models_loaded:
                            break
                    self.assertTrue(app._tunnel_models_loaded)
                    self.assertEqual(
                        app.screen.has_class("tunnel-narrow"), layout != "wide"
                    )
                    self.assertEqual(
                        app.screen.has_class("tunnel-tiny"), layout == "tiny"
                    )

                    actions = app.query_one("#tunnel-actions")
                    action_controls = [
                        app.query_one(selector)
                        for selector in (
                            "#tunnel-start",
                            "#tunnel-stop",
                            "#tunnel-refresh-models",
                            "#tunnel-copy-url",
                            "#tunnel-copy-key",
                            "#tunnel-rotate-key",
                            "#tunnel-provider-select",
                            "#tunnel-all-models",
                            "#tunnel-rpm-input",
                            "#tunnel-save-rpm",
                        )
                    ]
                    self.assertTrue(
                        all(
                            control.region.width > 0
                            and actions.region.x <= control.region.x
                            and control.region.right <= actions.region.right
                            for control in action_controls
                        )
                    )
                    self.assertEqual(
                        len({control.region.y for control in action_controls}),
                        {"tiny": 5, "narrow": 2, "wide": 1}[layout],
                    )
                    for index, control in enumerate(action_controls):
                        for other in action_controls[index + 1 :]:
                            self.assertFalse(control.region.overlaps(other.region))
                    for button in (
                        control for control in action_controls if isinstance(control, Button)
                    ):
                        self.assertGreaterEqual(
                            button.region.width, len(str(button.label)) + 4
                        )
                    provider_controls = action_controls[-4:]
                    self.assertEqual(
                        len({control.region.y for control in provider_controls}),
                        2 if layout == "tiny" else 1,
                    )
                    self.assertEqual(
                        app.query_one("#tunnel-provider-select", Select).value,
                        "echo",
                    )

                    profile = app.query_one("#tunnel-profile")
                    profile_controls = list(profile.children)
                    self.assertEqual(
                        len({control.region.y for control in profile_controls}),
                        2 if layout == "tiny" else 1,
                    )
                    self.assertTrue(
                        all(
                            profile.region.x <= control.region.x
                            and control.region.right <= profile.region.right
                            for control in profile_controls
                        )
                    )

                    tests = app.query_one("#tunnel-model-tests")
                    tests.scroll_visible(animate=False)
                    await pilot.pause()
                    test_controls = [
                        app.query_one("#tunnel-test-selected", Button),
                        app.query_one("#tunnel-test-all", Button),
                        app.query_one("#tunnel-model-test-status", Static),
                    ]
                    self.assertTrue(
                        all(
                            control.region.width > 0
                            and tests.region.x <= control.region.x
                            and control.region.right <= tests.region.right
                            for control in test_controls
                        )
                    )
                    self.assertEqual(
                        len({control.region.y for control in test_controls}),
                        2 if layout == "tiny" else 1,
                    )
                    models = app.query_one("#tunnel-models", SelectionList)
                    app._refresh_tunnel_model_probes()
                    models.scroll_visible(animate=False)
                    await pilot.pause()
                    prompt = str(models.get_option_at_index(0).prompt)
                    self.assertTrue(prompt.startswith("● Available  ·  provider/"))
                    rendered = "\n".join(
                        models.render_line(line).text
                        for line in range(models.region.height)
                    )
                    self.assertIn("Available", rendered)
                    self.assertNotIn("TAILMARKER", rendered)
                    if layout == "wide":
                        self.assertGreater(models.region.height, 8)
                        self.assertGreater(
                            app.query_one("#tunnel-clients", TunnelClientsTable).region.height,
                            8,
                        )

    async def test_external_tunnel_provider_change_clears_stale_blocked_catalog(self):
        server = _TunnelUIServer()
        app = RelayApp(server)
        async with app.run_test(size=(120, 35)) as pilot:
            app.query_one("#main-tabs").active = "tunnel"
            for _ in range(40):
                await pilot.pause(0.05)
                if app._tunnel_models_loaded:
                    break
            self.assertTrue(app._tunnel_models_loaded)
            server.probe_states = {"model-a": "available"}
            app._refresh_tunnel_model_probes()
            self.assertIn(
                "available",
                str(app.query_one("#tunnel-model-test-status", Static).content),
            )

            server.block_fetch_provider = "echo"
            server.fetch_started.clear()
            server.fetch_release.clear()
            app._load_tunnel_models()
            for _ in range(40):
                await pilot.pause(0.05)
                if server.fetch_started.is_set():
                    break
            self.assertTrue(server.fetch_started.is_set())

            server.provider_id = "local"
            server.allowed = ()
            app._load_tunnel_provider()
            models = app.query_one("#tunnel-models", SelectionList)
            self.assertEqual(models.option_count, 0)
            self.assertTrue(models.disabled)
            self.assertEqual(app._tunnel_model_probe_states, {})
            self.assertEqual(
                str(app.query_one("#tunnel-model-test-status", Static).content),
                "Models are not tested",
            )

            server.fetch_release.set()
            for _ in range(80):
                await pilot.pause(0.05)
                if app._tunnel_models_loaded and app._tunnel_provider_id == "local":
                    break
            self.assertTrue(app._tunnel_models_loaded)
            self.assertEqual(set(app._tunnel_models), set(server.catalogs["local"]))
            prompts = "\n".join(str(option.prompt) for option in models.options)
            for old_model in server.catalogs["echo"]:
                self.assertNotIn(old_model, prompts)

    async def test_tunnel_model_tests_selected_all_bounded_and_show_every_state(self):
        server = _TunnelUIServer()
        server.catalogs["echo"] = tuple(f"model-{letter}" for letter in "abcdef")
        server.allowed = ("model-a", "model-c")
        server.probe_states = {
            "model-a": "available",
            "model-b": "unavailable",
            "model-c": "timeout",
            "model-d": "testing",
        }
        app = RelayApp(server)
        async with app.run_test(size=(120, 35)) as pilot:
            app.query_one("#main-tabs").active = "tunnel"
            for _ in range(40):
                await pilot.pause(0.05)
                if app._tunnel_models_loaded:
                    break
            self.assertTrue(app._tunnel_models_loaded)
            app._refresh_tunnel_model_probes()

            models = app.query_one("#tunnel-models", SelectionList)
            prompts = "\n".join(
                str(models.get_option_at_index(index).prompt)
                for index in range(models.option_count)
            )
            for state in ("Available", "Unavailable", "Timeout", "Testing"):
                self.assertIn(state, prompts)
            status = str(app.query_one("#tunnel-model-test-status", Static).content)
            for state in ("available", "unavailable", "timeout", "testing"):
                self.assertIn(state, status)

            server.probe_calls.clear()
            server.probe_results.update(
                {"model-a": "available", "model-c": "timeout"}
            )
            await pilot.click("#tunnel-test-selected")
            for _ in range(80):
                await pilot.pause(0.05)
                if not app._tunnel_model_probe_busy:
                    break
            self.assertFalse(app._tunnel_model_probe_busy)
            self.assertEqual(set(server.probe_calls), {"model-a", "model-c"})
            self.assertEqual(len(server.probe_calls), 2)

            server.probe_calls.clear()
            server.probe_max_active = 0
            server.probe_block = True
            server.probe_started.clear()
            server.probe_release.clear()
            server.probe_results = {
                model: "available" for model in server.catalogs["echo"]
            }
            await pilot.click("#tunnel-test-all")
            for _ in range(40):
                await pilot.pause(0.05)
                if server.probe_max_active == 4:
                    break
            self.assertEqual(server.probe_max_active, 4)
            self.assertTrue(app._tunnel_model_probe_busy)
            self.assertTrue(app.query_one("#tunnel-test-selected", Button).disabled)
            self.assertTrue(app.query_one("#tunnel-test-all", Button).disabled)
            self.assertIn(
                "max 4 concurrent",
                str(app.query_one("#tunnel-model-test-status", Static).content),
            )
            server.probe_release.set()
            for _ in range(120):
                await pilot.pause(0.05)
                if not app._tunnel_model_probe_busy:
                    break
            self.assertFalse(app._tunnel_model_probe_busy)
            self.assertEqual(set(server.probe_calls), set(server.catalogs["echo"]))
            self.assertEqual(len(server.probe_calls), len(server.catalogs["echo"]))
            self.assertLessEqual(server.probe_max_active, 4)

    async def test_tunnel_model_test_worker_failures_always_clear_busy_state(self):
        class BrokenExecutor:
            def __init__(self, *args, **kwargs):
                pass

            def __enter__(self):
                return self

            def __exit__(self, *args):
                return False

            def submit(self, *args, **kwargs):
                raise RuntimeError("submit failed")

        server = _TunnelUIServer()
        app = RelayApp(server)
        async with app.run_test(size=(120, 35)) as pilot:
            app.query_one("#main-tabs").active = "tunnel"
            for _ in range(40):
                await pilot.pause(0.05)
                if app._tunnel_models_loaded:
                    break

            with patch("relay_ui.ThreadPoolExecutor", BrokenExecutor):
                await pilot.click("#tunnel-test-selected")
                for _ in range(40):
                    await pilot.pause(0.05)
                    if not app._tunnel_model_probe_busy:
                        break
            self.assertFalse(app._tunnel_model_probe_busy)
            self.assertIsNone(app._tunnel_model_probe_active_generation)
            self.assertFalse(app.query_one("#tunnel-test-selected", Button).disabled)

            with patch.object(app, "run_worker", side_effect=RuntimeError("schedule failed")):
                app._test_tunnel_models(all_models=False)
            self.assertFalse(app._tunnel_model_probe_busy)
            self.assertIsNone(app._tunnel_model_probe_active_generation)
            self.assertFalse(app.query_one("#tunnel-test-selected", Button).disabled)

    async def test_tunnel_model_probe_results_are_discarded_after_provider_change(self):
        server = _TunnelUIServer()
        server.probe_block = True
        app = RelayApp(server)
        async with app.run_test(size=(120, 35)) as pilot:
            app.query_one("#main-tabs").active = "tunnel"
            for _ in range(40):
                await pilot.pause(0.05)
                if app._tunnel_models_loaded:
                    break
            await pilot.click("#tunnel-test-selected")
            for _ in range(40):
                await pilot.pause(0.05)
                if server.probe_started.is_set():
                    break
            self.assertTrue(server.probe_started.is_set())

            app.query_one("#tunnel-provider-select", Select).value = "local"
            for _ in range(40):
                await pilot.pause(0.05)
                if server.provider_changes and app._tunnel_models_loaded:
                    break
            self.assertEqual(server.provider_changes, ["local"])
            server.probe_release.set()
            for _ in range(120):
                await pilot.pause(0.05)
                if not app._tunnel_model_probe_busy:
                    break
            self.assertFalse(app._tunnel_model_probe_busy)
            self.assertEqual(set(app._tunnel_models), set(server.catalogs["local"]))
            self.assertEqual(app._tunnel_model_probe_states, {})
            prompts = "\n".join(
                str(option.prompt)
                for option in app.query_one("#tunnel-models", SelectionList).options
            )
            for old_model in server.catalogs["echo"]:
                self.assertNotIn(old_model, prompts)

    async def test_shared_poll_keeps_actions_stable_and_resume_supports_stopped(self):
        server = _TunnelUIServer()
        app = RelayApp(server)
        async with app.run_test(size=(80, 24)) as pilot:
            app.query_one("#main-tabs").active = "shared-tunnels"
            for _ in range(40):
                await pilot.pause(0.05)
                if app._shared_tunnel_available and not app._shared_tunnel_refreshing:
                    break
            self.assertTrue(app._shared_tunnel_available)
            refresh = app.query_one("#shared-tunnel-refresh", Button)
            pause = app.query_one("#shared-tunnel-pause", Button)
            resume = app.query_one("#shared-tunnel-resume", Button)
            self.assertFalse(refresh.disabled)
            self.assertFalse(pause.disabled)

            server.shared_block = True
            server.shared_fetch_started.clear()
            server.shared_fetch_release.clear()
            app._refresh_shared_tunnels()
            for _ in range(40):
                await pilot.pause(0.05)
                if server.shared_fetch_started.is_set():
                    break
            self.assertTrue(server.shared_fetch_started.is_set())
            self.assertFalse(refresh.disabled)
            self.assertFalse(pause.disabled)

            await pilot.click("#shared-tunnel-pause")
            for _ in range(40):
                await pilot.pause(0.05)
                if server.shared_state == "paused" and not app._shared_tunnel_busy:
                    break
            self.assertEqual(server.shared_state, "paused")
            self.assertFalse(resume.disabled)
            server.shared_fetch_release.set()
            await pilot.pause(0.25)
            self.assertEqual(app._shared_tunnels[0]["state"], "paused")

            server.shared_block = False
            server.shared_state = "stopped"
            server.shared_revision += 1
            app._apply_shared_tunnels(server._shared_snapshot())
            self.assertFalse(resume.disabled)
            await pilot.click("#shared-tunnel-resume")
            for _ in range(40):
                await pilot.pause(0.05)
                if server.shared_state == "running" and not app._shared_tunnel_busy:
                    break
            self.assertEqual(server.shared_state, "running")
            self.assertFalse(pause.disabled)

    async def test_shared_control_captures_revision_before_delayed_worker_and_reorder(self):
        class ReorderingServer(_TunnelUIServer):
            def __init__(self):
                super().__init__()
                self.shared_items = [
                    {"name": "Ваш коннект", "state": "running"},
                    {"name": "Tunnel 1", "state": "running"},
                ]
                self.control_calls = []

            def _shared_snapshot(self):
                return {
                    "available": True,
                    "revision": self.shared_revision,
                    "tunnels": tuple(
                        {"position": position, **item}
                        for position, item in enumerate(self.shared_items)
                    ),
                }

            def control_shared_tunnel(self, position, revision, action):
                self.control_calls.append((position, revision, action))
                if revision != self.shared_revision:
                    raise ValueError("Stale shared tunnel")
                self.shared_items[position]["state"] = "paused"
                self.shared_revision += 1
                return self._shared_snapshot()

        server = ReorderingServer()
        app = RelayApp(server)
        async with app.run_test(size=(120, 35)) as pilot:
            app.query_one("#main-tabs").active = "shared-tunnels"
            for _ in range(40):
                await pilot.pause(0.05)
                if app._shared_tunnel_available and not app._shared_tunnel_refreshing:
                    break
            self.assertEqual(app._selected_shared_tunnel_position, 0)
            self.assertEqual(app._shared_tunnel_revision, 1)

            delayed_workers = []
            real_run_worker = app.run_worker
            with patch.object(
                app,
                "run_worker",
                side_effect=lambda work, **kwargs: delayed_workers.append((work, kwargs)),
            ):
                app._control_shared_tunnel("pause")
            self.assertEqual(len(delayed_workers), 1)
            self.assertTrue(app._shared_tunnel_busy)

            server.shared_items.reverse()
            server.shared_revision = 2
            app._apply_shared_tunnels(server._shared_snapshot())
            self.assertEqual(app._shared_tunnels[0]["name"], "Tunnel 1")

            work, kwargs = delayed_workers[0]
            real_run_worker(work, **kwargs)
            for _ in range(40):
                await pilot.pause(0.05)
                if server.control_calls and not app._shared_tunnel_busy:
                    break
            self.assertEqual(server.control_calls, [(0, 1, "pause")])
            self.assertEqual(
                [item["state"] for item in server.shared_items],
                ["running", "running"],
            )
            self.assertEqual(app._shared_tunnel_revision, 2)

    async def test_stale_unavailable_shared_poll_cannot_erase_successful_control(self):
        server = _TunnelUIServer()
        app = RelayApp(server)
        async with app.run_test(size=(80, 24)) as pilot:
            app.query_one("#main-tabs").active = "shared-tunnels"
            for _ in range(40):
                await pilot.pause(0.05)
                if app._shared_tunnel_available and not app._shared_tunnel_refreshing:
                    break
            self.assertTrue(app._shared_tunnel_available)

            server.shared_available = False
            server.shared_block = True
            server.shared_fetch_started.clear()
            server.shared_fetch_release.clear()
            app._refresh_shared_tunnels()
            for _ in range(40):
                await pilot.pause(0.05)
                if server.shared_fetch_started.is_set():
                    break
            self.assertTrue(server.shared_fetch_started.is_set())
            stale_generation = app._shared_tunnel_refresh_generation

            server.shared_available = True
            await pilot.click("#shared-tunnel-pause")
            for _ in range(40):
                await pilot.pause(0.05)
                if server.shared_state == "paused" and not app._shared_tunnel_busy:
                    break
            self.assertEqual(server.shared_state, "paused")
            self.assertGreater(app._shared_tunnel_refresh_generation, stale_generation)
            self.assertTrue(app._shared_tunnel_available)
            self.assertEqual(app._shared_tunnels[0]["state"], "paused")

            server.shared_fetch_release.set()
            await pilot.pause(0.35)
            self.assertTrue(app._shared_tunnel_available)
            self.assertEqual(app._shared_tunnel_revision, server.shared_revision)
            self.assertEqual(app._shared_tunnels[0]["state"], "paused")
            self.assertEqual(
                app.query_one("#shared-tunnels-table", DataTable).row_count, 1
            )
            self.assertFalse(app.query_one("#shared-tunnel-resume", Button).disabled)

    async def test_tunnel_status_tracks_own_shared_control_without_stale_override(self):
        server = _TunnelUIServer()
        server.state = "running"
        server.tunnel_url = (
            "https://luxuryprivate.duckdns.org/model-tunnel/" + "a" * 48 + "/v1"
        )
        app = RelayApp(server)
        async with app.run_test(size=(120, 35)) as pilot:
            app.query_one("#main-tabs").active = "tunnel"
            await pilot.pause(0.65)
            status = app.query_one("#tunnel-status", Static)
            self.assertIn("Online", str(status.content))

            for shared_state, expected in (("paused", "Paused"), ("stopped", "Stopped")):
                server.shared_state = shared_state
                server.shared_revision += 1
                for _ in range(60):
                    app._poll_shared_tunnels()
                    await pilot.pause(0.05)
                    if expected in str(status.content):
                        break
                self.assertIn(expected, str(status.content))
                self.assertNotIn("Online", str(status.content))
                self.assertTrue(app.query_one("#tunnel-copy-url", Button).disabled)
                if shared_state == "paused":
                    await pilot.pause(2.1)  # Exercise the Tunnel-only polling timer.
                    self.assertIn("Paused", str(status.content))

            server.shared_available = False
            for _ in range(60):
                app._poll_shared_tunnels()
                await pilot.pause(0.05)
                if not app._shared_tunnel_available and "Online" in str(status.content):
                    break
            self.assertFalse(app._shared_tunnel_available)
            self.assertIn("Online", str(status.content))
            self.assertNotIn("Stopped", str(status.content))
            self.assertFalse(app.query_one("#tunnel-copy-url", Button).disabled)

    @staticmethod
    def _table_text(table: DataTable) -> str:
        values = []
        for row in range(table.row_count):
            for cell in table.get_row_at(row):
                values.append(cell.plain if isinstance(cell, Text) else str(cell))
        return "\n".join(values)

    @staticmethod
    def _style_at(text: Text, value: str) -> str:
        offset = text.plain.index(value)
        return next(
            str(span.style)
            for span in text.spans
            if span.start <= offset < span.end
        )


if __name__ == "__main__":
    unittest.main()

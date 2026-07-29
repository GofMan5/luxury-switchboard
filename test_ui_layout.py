import unittest

from rich.text import Text
from textual.widgets import Button, DataTable, Static

from relay_ui import ActivityTable, PALETTE, ProviderFormScreen, RelayApp


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


class RelayUILayoutTest(unittest.IsolatedAsyncioTestCase):
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

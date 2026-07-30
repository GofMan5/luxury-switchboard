"""Production Textual UI for Provider Switchboard.

The module deliberately has no entrypoint. Instantiate ``RelayApp`` with a
running ``RelayServer``.
"""

from __future__ import annotations

import re
from collections.abc import Mapping
from concurrent.futures import ThreadPoolExecutor, as_completed
from http import HTTPStatus
from ipaddress import ip_address
from typing import Any

from rich.text import Text
from textual import events, on
from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.containers import Container, Grid, Horizontal, VerticalScroll
from textual.css.query import NoMatches
from textual.message import Message
from textual.screen import ModalScreen
from textual.theme import Theme
from textual.widgets import (
    Button,
    Checkbox,
    DataTable,
    Input,
    OptionList,
    SelectionList,
    Select,
    Static,
    TabbedContent,
    TabPane,
)


PALETTE = {
    "background": "#101113",
    "surface": "#181a1d",
    "border": "#30343a",
    "foreground": "#d8dade",
    "secondary": "#8b9098",
    "muted": "#62676f",
    "live": "#5b9bd5",
    "success": "#59ad7a",
    "waiting": "#c49a52",
    "error": "#cf6666",
}

MONO_THEME = Theme(
    name="switchboard-mono",
    primary=PALETTE["foreground"],
    secondary=PALETTE["secondary"],
    warning="#b5b8bd",
    error="#d8dade",
    success="#d8dade",
    accent="#a8adb4",
    foreground=PALETTE["foreground"],
    background=PALETTE["background"],
    surface=PALETTE["surface"],
    panel=PALETTE["surface"],
    dark=True,
)

AUTH_MODES = (
    ("Automatic", "auto"),
    ("Pass through", "passthrough"),
    ("Bearer", "bearer"),
    ("x-api-key", "x-api-key"),
)
PERIODS = (("24h", "24h"), ("48h", "48h"), ("72h", "72h"), ("All", "all"))
TOKEN_FIELDS = (
    "input_tokens",
    "context_tokens",
    "output_tokens",
    "cached_tokens",
    "reasoning_tokens",
    "total_tokens",
)
TUNNEL_PUBLIC_URL_PATTERN = re.compile(
    r"https://luxuryprivate\.duckdns\.org/model-tunnel/[0-9a-f]{48}/v1"
)
TUNNEL_SAFE_ERRORS = frozenset(
    {
        "Tunnel SSH client is not installed",
        "Tunnel publisher key is not installed",
        "Tunnel host trust could not be prepared",
        "Tunnel SSH client could not start",
        "Tunnel publisher profile is already active",
        "Tunnel publisher key was rejected",
        "Tunnel host verification failed",
        "Tunnel VPS is unreachable",
        "Tunnel SSH connection failed",
        "Tunnel public route did not become ready",
        "Tunnel could not start",
        "Tunnel connection stopped",
    }
)
SHARED_TUNNEL_NAME_PATTERN = re.compile(
    r"(?:Ваш коннект|Tunnel [1-9][0-9]{0,3})\Z"
)
SHARED_TUNNEL_SELF_NAME = "Ваш коннект"
TUNNEL_MODEL_PROBE_STATES = frozenset(
    {"testing", "available", "unavailable", "timeout"}
)


def _value(item: Any, name: str, default: Any = None) -> Any:
    if isinstance(item, Mapping):
        return item.get(name, default)
    return getattr(item, name, default)


def _integer(value: Any, default: int = 0) -> int:
    try:
        return int(value)
    except (TypeError, ValueError, OverflowError):
        return default


def _number(value: Any, default: float = 0.0) -> float:
    try:
        return float(value)
    except (TypeError, ValueError, OverflowError):
        return default


def _short(value: Any, limit: int) -> str:
    text = str(value if value not in (None, "") else "—")
    return text if len(text) <= limit else text[: max(1, limit - 1)] + "…"


def _count(value: Any) -> str:
    number = _integer(value)
    if abs(number) >= 1_000_000_000:
        return f"{number / 1_000_000_000:.1f}b"
    if abs(number) >= 1_000_000:
        return f"{number / 1_000_000:.1f}m"
    if abs(number) >= 1_000:
        return f"{number / 1_000:.1f}k"
    return str(number)


def _milliseconds(value: Any) -> str:
    milliseconds = max(0.0, _number(value))
    return f"{milliseconds / 1000:.1f}s" if milliseconds >= 1000 else f"{milliseconds:.0f}ms"


def _rpm(value: Any, *, unlimited: bool = False) -> str:
    rpm = max(0.0, _number(value))
    if unlimited and rpm == 0:
        return "Unlimited"
    return f"{rpm:.1f}" if rpm % 1 else str(int(rpm))


def _token_speed(value: Any) -> str:
    speed = max(0.0, _number(value))
    return f"{speed:.1f}/s" if speed else "—"


def _tunnel_public_url(snapshot: Mapping[str, Any]) -> str:
    state = str(snapshot.get("state") or "").casefold()
    url = str(snapshot.get("url") or "")
    return url if (
        state in {"active", "online", "ready", "running", "started"}
        and snapshot.get("ready") is not False
        and TUNNEL_PUBLIC_URL_PATTERN.fullmatch(url)
    ) else ""


def _cell(value: Any, limit: int | None = None) -> Text:
    text = str(value if value not in (None, "") else "—")
    if limit is not None:
        text = _short(text, limit)
    return Text(text, no_wrap=True, overflow="ellipsis")


def _state_visual(value: Any) -> tuple[str, str]:
    state = str(value or "").casefold()
    return {
        "reading": (PALETTE["live"], "Reading"),
        "active": (PALETTE["live"], "Live"),
        "queued": (PALETTE["waiting"], "Queued"),
        "retry": (PALETTE["waiting"], "Retrying"),
        "ok": (PALETTE["success"], "Done"),
        "success": (PALETTE["success"], "Done"),
        "error": (PALETTE["error"], "Error"),
        "cancelled": (PALETTE["muted"], "Cancelled"),
        "connected": (PALETTE["success"], "Connected"),
        "idle": (PALETTE["muted"], "Idle"),
        "offline": (PALETTE["muted"], "Offline"),
    }.get(state, (PALETTE["muted"], state.title() or "—"))


def _state_cell(value: Any) -> Text:
    color, label = _state_visual(value)
    text = Text("● ", style=color)
    text.append(label, style=PALETTE["foreground"])
    return text


def _http_status_code(value: Any) -> int:
    try:
        return int(str(value).split(maxsplit=1)[0])
    except (TypeError, ValueError, OverflowError):
        return -1


def _http_status_text(value: Any) -> str:
    status = _http_status_code(value)
    if status < 0:
        return "—"
    try:
        return f"{status} {HTTPStatus(status).phrase}"
    except ValueError:
        return str(status)


def _http_status_color(value: Any) -> str:
    status = _http_status_code(value)
    if 200 <= status < 400:
        return PALETTE["success"]
    if status >= 400:
        return PALETTE["error"]
    return PALETTE["muted"]


def _http_status_cell(value: Any) -> Text:
    text = _http_status_text(value)
    return Text(
        _short(text, 20),
        style=_http_status_color(value),
        no_wrap=True,
        overflow="ellipsis",
    )


def _shared_tunnel_state_cell(value: Any) -> Text:
    state = str(value or "").casefold()
    color, label = {
        "running": (PALETTE["success"], "Running"),
        "paused": (PALETTE["waiting"], "Paused"),
        "stopped": (PALETTE["muted"], "Stopped"),
    }.get(state, (PALETTE["error"], "Unknown"))
    text = Text("● ", style=color)
    text.append(label, style=PALETTE["foreground"])
    return text


def _tunnel_model_prompt(model: str, state: str = "") -> Text:
    visual = {
        "testing": (PALETTE["live"], "Testing"),
        "available": (PALETTE["success"], "Available"),
        "unavailable": (PALETTE["error"], "Unavailable"),
        "timeout": (PALETTE["waiting"], "Timeout"),
    }.get(state)
    text = Text(no_wrap=True, overflow="ellipsis")
    if visual:
        color, label = visual
        text.append("● ", style=color)
        text.append(label, style=color)
        text.append("  ·  ", style=PALETTE["muted"])
    text.append(model)
    return text


def _operation_error(error: Exception) -> str:
    if isinstance(error, ValueError):
        return str(error) or "Invalid value"
    if isinstance(error, KeyError):
        return str(error).strip("'") or "Item not found"
    return "Operation failed"


def _tunnel_error(value: Any, fallback: str = "Tunnel operation failed") -> str:
    text = str(value or "")
    return text if text in TUNNEL_SAFE_ERRORS else fallback


def _metric(title: str, primary: str, secondary: str) -> Text:
    text = Text()
    text.append(title + "\n", style=PALETTE["secondary"])
    text.append(primary + "\n", style=f"bold {PALETTE['foreground']}")
    text.append(secondary, style=PALETTE["muted"])
    return text


DETAIL_FIELDS = (
    ("Time", "time"),
    ("Provider", "provider"),
    ("Model", "model"),
    ("Method", "method"),
    ("Path", "path"),
    ("State", "state"),
    ("Status", "status"),
    ("Latency", "latency_ms"),
    ("Queue", "queue_ms"),
    ("Retries", "retries"),
    ("429 retries", "retries_429"),
    ("Context tokens", "context_tokens"),
    ("Input tokens", "input_tokens"),
    ("Output tokens", "output_tokens"),
    ("Cached tokens", "cached_tokens"),
    ("Reasoning tokens", "reasoning_tokens"),
    ("Total tokens", "total_tokens"),
    ("Token speed", "tokens_per_second"),
    ("Bytes in", "bytes_in"),
    ("Bytes out", "bytes_out"),
    ("Cache TTL", "cache_1h"),
)

TUNNEL_EVENT_STATES = frozenset(
    {"queued", "active", "reading", "retry", "success", "ok", "error", "cancelled"}
)
TUNNEL_EVENT_FIELDS = (
    ("State", "state"),
    ("Method", "method"),
    ("Path", "path"),
    ("Model", "model"),
    ("Status", "status"),
    ("Latency", "latency"),
    ("Bytes", "bytes"),
)


def _safe_event_detail(event: Mapping[str, Any]) -> dict[str, str]:
    """Copy only fields explicitly safe for display."""
    detail: dict[str, str] = {}
    for label, field in DETAIL_FIELDS:
        value = event.get(field)
        if field == "cache_1h":
            value = "1 hour" if value else "Off"
        elif field in {"latency_ms", "queue_ms"}:
            value = _milliseconds(value)
        elif field == "tokens_per_second":
            value = _token_speed(value)
        elif field == "status":
            value = _http_status_text(value)
        detail[label] = str(value if value not in (None, "") else "—")
    return detail


def _tunnel_ip(value: Any) -> str:
    """Return a canonical IP address without accepting arbitrary display text."""
    try:
        return str(ip_address(str(value or "").strip()))
    except ValueError:
        return ""


def _safe_tunnel_event(event: Any) -> dict[str, str] | None:
    """Normalize the public-client event allowlist; ignore every other field."""
    if not isinstance(event, Mapping):
        return None
    method = str(event.get("method") or "").upper()
    method = method if re.fullmatch(r"[A-Z]{1,12}", method) else "—"

    path = str(event.get("path") or "").split("?", 1)[0].split("#", 1)[0]
    path = path if path.startswith("/") and not any(ord(char) < 32 for char in path) else "—"

    model = str(event.get("model") or "")
    model = model if model and not any(ord(char) < 32 for char in model) else "—"

    state = str(event.get("state") or "").casefold()
    state = state if state in TUNNEL_EVENT_STATES else ""
    status = _http_status_code(event.get("status"))
    status_text = _http_status_text(status) if 100 <= status <= 599 else "—"
    latency = max(0.0, _number(event.get("latency_ms")))
    request_bytes = max(0, _integer(event.get("request_bytes", event.get("bytes_in"))))
    response_bytes = max(0, _integer(event.get("response_bytes", event.get("bytes_out"))))
    if not request_bytes and not response_bytes:
        response_bytes = max(0, _integer(event.get("bytes")))
    return {
        "State": state or "—",
        "Method": _short(method, 12),
        "Path": _short(path, 160),
        "Model": _short(model, 80),
        "Status": status_text,
        "Latency": _milliseconds(latency),
        "Bytes": f"{_count(request_bytes)} in · {_count(response_bytes)} out",
    }


def _tunnel_clients(source: Any) -> list[dict[str, Any]] | None:
    """Normalize local tunnel telemetry while preserving unavailable vs empty."""
    top_events: list[Mapping[str, Any]] = []
    if isinstance(source, Mapping):
        if "clients" in source:
            raw_clients = source.get("clients")
            for field in ("recent", "live"):
                values = source.get(field)
                if isinstance(values, (list, tuple)):
                    top_events.extend(value for value in values if isinstance(value, Mapping))
        elif not source or all(isinstance(value, Mapping) for value in source.values()):
            raw_clients = source
        else:
            return None
    else:
        raw_clients = source

    if isinstance(raw_clients, Mapping):
        client_items = list(raw_clients.items())
    elif isinstance(raw_clients, (list, tuple)):
        client_items = list(enumerate(raw_clients))
    else:
        return None

    events_by_ip: dict[str, list[Mapping[str, Any]]] = {}
    for event in top_events:
        event_ip = _tunnel_ip(event.get("ip"))
        if event_ip:
            events_by_ip.setdefault(event_ip, []).append(event)

    clients: dict[str, dict[str, Any]] = {}
    for fallback_identity, raw in client_items:
        if not isinstance(raw, Mapping):
            continue
        client_ip = _tunnel_ip(raw.get("ip"))
        if not client_ip:
            continue
        identity = str(raw.get("client_id", raw.get("id", fallback_identity)))
        row_key = f"client:{identity}:{client_ip}"
        direct_events = raw.get("events")
        event_values = (
            [value for value in direct_events if isinstance(value, Mapping)]
            if isinstance(direct_events, (list, tuple))
            else []
        )
        event_values.extend(events_by_ip.get(client_ip, ()))
        safe_events = [
            safe for safe in (_safe_tunnel_event(value) for value in event_values[-80:]) if safe
        ]
        idle_ms = max(0.0, _number(raw.get("idle_ms")))
        active = max(0, _integer(raw.get("active")))
        connected = max(0, _integer(raw.get("connected")))
        raw_state = str(raw.get("state") or "").casefold()
        if raw_state in {"active", "connected", "idle", "offline"}:
            state = raw_state
        elif active:
            state = "active"
        elif connected:
            state = "connected"
        else:
            state = "offline"
        clients[row_key] = {
            "key": row_key,
            "ip": client_ip,
            "actual_rpm": max(0, _integer(raw.get("actual_rpm", raw.get("rpm")))),
            "connected": connected,
            "active": active,
            "queued": max(0, connected - active),
            "last_seen": "now" if idle_ms < 1000 else f"{_milliseconds(idle_ms)} ago",
            "state": state,
            "events": safe_events[-80:],
        }
    return list(clients.values())


def _read_tunnel_clients(server: Any) -> list[dict[str, Any]] | None:
    dedicated = getattr(server, "tunnel_clients_snapshot", None)
    if callable(dedicated):
        try:
            clients = _tunnel_clients(dedicated())
        except Exception:
            clients = None
        if clients is not None:
            return clients
    try:
        return _tunnel_clients(server.tunnel_snapshot())
    except Exception:
        return None


class ActivityTable(DataTable):
    """Activity table which opens sanitized details on double-click or Enter."""

    BINDINGS = [Binding("enter", "show_detail", "Request details", show=False)]

    class DetailRequested(Message):
        def __init__(self, table: "ActivityTable", detail: dict[str, str]) -> None:
            self.table = table
            self.detail = detail
            super().__init__()

        @property
        def control(self) -> "ActivityTable":
            return self.table

    def __init__(self, *args, **kwargs) -> None:
        super().__init__(*args, **kwargs)
        self._details: dict[str, dict[str, str]] = {}

    def set_detail(self, row_key: str, detail: dict[str, str]) -> None:
        self._details[row_key] = detail

    def forget_detail(self, row_key: str) -> None:
        self._details.pop(row_key, None)

    def _request_detail(self, coordinate: Any) -> bool:
        if not self.is_valid_coordinate(coordinate):
            return False
        row_key = self.coordinate_to_cell_key(coordinate).row_key.value
        detail = self._details.get(str(row_key))
        if detail is None:
            return False
        self.post_message(self.DetailRequested(self, detail))
        return True

    def action_show_detail(self) -> None:
        if self.row_count:
            self._request_detail(self.cursor_coordinate)

    def on_click(self, event: events.Click) -> None:
        if event.chain != 2 or not self.row_count:
            return
        coordinate = self.hover_coordinate
        if not self.is_valid_coordinate(coordinate):
            coordinate = self.cursor_coordinate
        if self._request_detail(coordinate):
            event.stop()


class TunnelClientsTable(DataTable):
    """Tunnel clients table with keyboard and mouse detail activation."""

    BINDINGS = [Binding("enter", "show_client", "Client activity", show=False)]

    class ClientRequested(Message):
        def __init__(self, table: "TunnelClientsTable", client: Mapping[str, Any]) -> None:
            self.table = table
            self.client = dict(client)
            super().__init__()

        @property
        def control(self) -> "TunnelClientsTable":
            return self.table

    def __init__(self, *args, **kwargs) -> None:
        super().__init__(*args, **kwargs)
        self._clients: dict[str, dict[str, Any]] = {}

    def set_client(self, row_key: str, client: Mapping[str, Any]) -> None:
        self._clients[row_key] = dict(client)

    def forget_client(self, row_key: str) -> None:
        self._clients.pop(row_key, None)

    def _request_client(self, coordinate: Any) -> bool:
        if not self.is_valid_coordinate(coordinate):
            return False
        row_key = self.coordinate_to_cell_key(coordinate).row_key.value
        client = self._clients.get(str(row_key))
        if client is None:
            return False
        self.post_message(self.ClientRequested(self, client))
        return True

    def action_show_client(self) -> None:
        if self.row_count:
            self._request_client(self.cursor_coordinate)

    def on_click(self, event: events.Click) -> None:
        if event.chain != 2 or not self.row_count:
            return
        coordinate = self.hover_coordinate
        if not self.is_valid_coordinate(coordinate):
            coordinate = self.cursor_coordinate
        if self._request_client(coordinate):
            event.stop()


MODAL_CSS = f"""
ModalScreen {{
    align: center middle;
    background: #000000 70%;
    color: {PALETTE['foreground']};
}}

.modal {{
    width: 72;
    max-width: 96%;
    height: auto;
    max-height: 95%;
    padding: 1 2;
    overflow-y: auto;
    border: solid {PALETTE['border']};
    background: {PALETTE['surface']};
}}

.modal-title {{
    height: 2;
    text-style: bold;
    color: {PALETTE['foreground']};
}}

.modal-label {{
    height: 1;
    margin-top: 1;
    color: {PALETTE['secondary']};
}}

.modal-error {{
    height: 2;
    color: {PALETTE['foreground']};
    text-style: bold;
}}

.modal-actions {{
    height: 3;
    margin-top: 1;
    align-horizontal: right;
}}

.modal-actions Button {{
    width: 14;
    margin-left: 1;
}}

Input {{
    height: 3;
    border: solid {PALETTE['border']};
    background: {PALETTE['background']};
    color: {PALETTE['foreground']};
}}

Input:focus {{
    border: solid #777d86;
}}

#provider-auth {{
    height: 7;
    grid-size: 2 2;
    grid-columns: 1fr 1fr;
    grid-rows: 3 3;
    grid-gutter: 1 1;
}}

#provider-auth Button {{ width: 1fr; min-width: 0; margin: 0; }}
#provider-auth Button.provider-auth-selected {{
    border: solid #777d86;
    background: #292d32;
    color: #f0f1f2;
    text-style: bold;
}}

Checkbox {{
    height: 3;
    color: {PALETTE['secondary']};
}}

Checkbox > .toggle--button {{ color: {PALETTE['secondary']}; }}
Checkbox.-on > .toggle--button {{ color: {PALETTE['foreground']}; }}

Button {{
    height: 3;
    border: solid #34383e;
    background: {PALETTE['surface']};
    color: #b9bdc3;
}}

Button:hover {{
    border: solid #555b64;
    background: #22252a;
    color: #f0f1f2;
}}

Button:focus {{ border: solid #777d86; }}

.tunnel-client-modal {{
    width: 96%;
    height: 82%;
    min-height: 12;
    max-height: 95%;
    padding: 1;
    overflow: hidden;
}}

#tunnel-client-summary {{
    height: 2;
    color: {PALETTE['secondary']};
}}

#tunnel-client-events {{ height: 1fr; min-height: 5; }}
"""


class _Modal(ModalScreen):
    BINDINGS = [Binding("escape", "cancel", "Cancel", show=False)]
    CSS = MODAL_CSS

    def action_cancel(self) -> None:
        self.dismiss(None)


class EventDetailScreen(_Modal):
    def __init__(self, detail: dict[str, str]) -> None:
        super().__init__()
        self.detail = detail

    def compose(self) -> ComposeResult:
        text = Text()
        for label, _field in DETAIL_FIELDS:
            value = self.detail.get(label, "—")
            style = PALETTE["foreground"]
            if label == "State":
                style, value = _state_visual(value)
            elif label == "Status":
                style = _http_status_color(value)
            text.append(f"{label:<17}", style=PALETTE["secondary"])
            text.append(value + "\n", style=style)
        with Container(classes="modal", id="event-detail-dialog"):
            yield Static("Request details", classes="modal-title")
            yield Static(text, id="event-detail")
            with Horizontal(classes="modal-actions"):
                yield Button("Close", id="detail-close")

    @on(Button.Pressed, "#detail-close")
    def _close(self) -> None:
        self.dismiss(None)


class TunnelClientScreen(_Modal):
    """Auto-refreshing, bounded, strictly sanitized client activity."""

    def __init__(self, server: Any, client: Mapping[str, Any]) -> None:
        super().__init__()
        self.server = server
        self.client_key = str(client.get("key") or "")
        self.client_ip = _tunnel_ip(client.get("ip"))
        self._event_signature: tuple[tuple[str, ...], ...] = ()

    def compose(self) -> ComposeResult:
        with Container(classes="modal tunnel-client-modal", id="tunnel-client-dialog"):
            yield Static(f"Tunnel client · {self.client_ip or 'unavailable'}", classes="modal-title")
            yield Static("Loading safe activity…", id="tunnel-client-summary")
            yield DataTable(id="tunnel-client-events")
            with Horizontal(classes="modal-actions"):
                yield Button("Close", id="tunnel-client-close")

    def on_mount(self) -> None:
        table = self.query_one("#tunnel-client-events", DataTable)
        for label, key, width in (
            ("State", "state", 11),
            ("Method", "method", 8),
            ("Path", "path", 28),
            ("Model", "model", 22),
            ("Status", "status", 20),
            ("Latency", "latency", 10),
            ("Bytes", "bytes", 20),
        ):
            table.add_column(label, key=key, width=width)
        table.cursor_type = "row"
        table.zebra_stripes = True
        self._refresh_client()
        self.set_interval(0.5, self._refresh_client)

    def _snapshot(self) -> list[dict[str, Any]] | None:
        return _read_tunnel_clients(self.server)

    def _refresh_client(self) -> None:
        clients = self._snapshot()
        client = next(
            (
                item
                for item in clients or ()
                if item["key"] == self.client_key or item["ip"] == self.client_ip
            ),
            None,
        )
        table = self.query_one("#tunnel-client-events", DataTable)
        if clients is None:
            self.query_one("#tunnel-client-summary", Static).update(
                "Client activity unavailable · retrying automatically"
            )
            table.border_title = "Safe events unavailable"
            events = []
        elif client is None:
            self.query_one("#tunnel-client-summary", Static).update(
                f"{self.client_ip or 'Client'} · no longer connected"
            )
            table.border_title = "No recent safe events"
            events = []
        else:
            self.client_key = client["key"]
            self.query_one("#tunnel-client-summary", Static).update(
                f"{client['actual_rpm']} RPM · {client['connected']} total · "
                f"{client['active']} active · "
                f"{client['queued']} queued · {client['state']} · "
                f"last seen {client['last_seen']}"
            )
            events = client["events"][-80:]
            table.border_title = (
                f"Latest {len(events)} safe event{'s' if len(events) != 1 else ''} · max 80"
                if events
                else "No recent safe events"
            )

        signature = tuple(
            tuple(event[label] for label, _field in TUNNEL_EVENT_FIELDS)
            for event in events
        )
        if signature == self._event_signature:
            return
        follow = not table.row_count or table.scroll_y >= table.max_scroll_y
        scroll_x, scroll_y, cursor_row = table.scroll_x, table.scroll_y, table.cursor_row
        table.clear()
        for index, event in enumerate(events):
            table.add_row(
                _state_cell(event["State"]),
                _cell(event["Method"], 12),
                _cell(event["Path"], 160),
                _cell(event["Model"], 80),
                _http_status_cell(event["Status"]),
                _cell(event["Latency"], 10),
                _cell(event["Bytes"], 20),
                key=f"event:{index}",
            )
        self._event_signature = signature
        if not events:
            return
        if follow:
            table.move_cursor(row=len(events) - 1, column=0, animate=False)
            table.scroll_end(animate=False)
        else:
            table.move_cursor(row=min(cursor_row, len(events) - 1), column=0, animate=False)
            table.scroll_to(scroll_x, scroll_y, animate=False, immediate=True)

    @on(Button.Pressed, "#tunnel-client-close")
    def _close(self) -> None:
        self.dismiss(None)


class ProviderFormScreen(_Modal):
    def __init__(self, provider: Any | None = None) -> None:
        super().__init__()
        self.provider = provider
        value = str(_value(provider, "auth_mode", "auto"))
        self.auth_mode = value if value in {item for _, item in AUTH_MODES} else "auto"

    def compose(self) -> ComposeResult:
        editing = self.provider is not None
        with Container(classes="modal", id="provider-form-dialog"):
            yield Static("Edit provider" if editing else "Add provider", classes="modal-title")
            yield Static("Name", classes="modal-label")
            yield Input(value=str(_value(self.provider, "name", "")), id="provider-name")
            yield Static("URL", classes="modal-label")
            yield Input(
                value=str(_value(self.provider, "upstream", "")),
                placeholder="https://provider.example/v1",
                id="provider-url",
            )
            yield Static("Authentication", classes="modal-label")
            with Grid(id="provider-auth"):
                for label, value in AUTH_MODES:
                    yield Button(
                        label,
                        id=f"provider-auth-{value}",
                        classes=(
                            "provider-auth-choice provider-auth-selected"
                            if value == self.auth_mode
                            else "provider-auth-choice"
                        ),
                    )
            yield Static("Provider cap RPM · 0 = use key limits", classes="modal-label")
            yield Input(
                value=str(_integer(_value(self.provider, "rpm", 0))),
                type="integer",
                restrict=r"\d*",
                id="provider-rpm",
            )
            yield Checkbox(
                "Extend supported cache breakpoints to 1 hour",
                value=bool(_value(self.provider, "cache_1h", False)),
                id="provider-cache",
            )
            yield Static(id="provider-form-error", classes="modal-error")
            with Horizontal(classes="modal-actions"):
                yield Button("Save", id="provider-save")
                yield Button("Cancel", id="provider-cancel")

    def on_mount(self) -> None:
        self.query_one("#provider-name", Input).focus()

    @on(Button.Pressed, "#provider-save")
    def _save(self) -> None:
        name = self.query_one("#provider-name", Input).value.strip()
        upstream = self.query_one("#provider-url", Input).value.strip()
        rpm_text = self.query_one("#provider-rpm", Input).value
        try:
            rpm = int(rpm_text or "0")
            if rpm < 0:
                raise ValueError
        except ValueError:
            self.query_one("#provider-form-error", Static).update("RPM must be 0 or greater")
            return
        if not name or not upstream:
            self.query_one("#provider-form-error", Static).update("Name and URL are required")
            return
        self.dismiss(
            {
                "name": name,
                "upstream": upstream,
                "auth_mode": self.auth_mode,
                "rpm": rpm,
                "cache_1h": self.query_one("#provider-cache", Checkbox).value,
            }
        )

    @on(Button.Pressed, ".provider-auth-choice")
    def _choose_auth(self, event: Button.Pressed) -> None:
        value = str(event.button.id or "").removeprefix("provider-auth-")
        if value not in {item for _, item in AUTH_MODES}:
            return
        self.auth_mode = value
        for button in self.query(".provider-auth-choice"):
            button.set_class(button is event.button, "provider-auth-selected")

    @on(Button.Pressed, "#provider-cancel")
    def _cancel(self) -> None:
        self.dismiss(None)


class ConfirmDeleteScreen(_Modal):
    def __init__(self, provider_name: str) -> None:
        super().__init__()
        self.provider_name = provider_name

    def compose(self) -> ComposeResult:
        with Container(classes="modal", id="confirm-dialog"):
            yield Static("Delete provider", classes="modal-title")
            yield Static(Text(f"Delete {self.provider_name}? Requests using it are cancelled."))
            with Horizontal(classes="modal-actions"):
                yield Button("Delete", id="confirm-delete")
                yield Button("Cancel", id="confirm-cancel")

    @on(Button.Pressed, "#confirm-delete")
    def _confirm(self) -> None:
        self.dismiss(True)

    @on(Button.Pressed, "#confirm-cancel")
    def _cancel(self) -> None:
        self.dismiss(False)


class ModelsScreen(_Modal):
    def __init__(self, server: Any, provider_id: str, provider_name: str) -> None:
        super().__init__()
        self.server = server
        self.provider_id = provider_id
        self.provider_name = provider_name
        self._models: list[str] = []

    def compose(self) -> ComposeResult:
        with Container(classes="modal", id="models-dialog"):
            yield Static(Text(f"Models · {self.provider_name}"), classes="modal-title")
            yield Static("Loading…", id="models-status")
            yield Input(placeholder="Filter models", disabled=True, id="models-filter")
            yield OptionList(id="models-list", markup=False)
            with Horizontal(classes="modal-actions"):
                yield Button("Close", id="models-close")

    def on_mount(self) -> None:
        self.run_worker(self._load_models, thread=True, group="models", exclusive=True)

    def _load_models(self) -> None:
        try:
            result = self.server.fetch_models(self.provider_id)
            models = self._model_names(result)
        except Exception:
            self.app.call_from_thread(self._show_models, [], True)
        else:
            self.app.call_from_thread(self._show_models, models, False)

    @staticmethod
    def _model_names(result: Any) -> list[str]:
        if isinstance(result, Mapping):
            result = result.get("data", result.get("models", ()))
        if isinstance(result, (str, bytes)) or result is None:
            result = (result,) if isinstance(result, str) else ()
        names: list[str] = []
        for item in result:
            if isinstance(item, str):
                name = item
            elif isinstance(item, Mapping):
                name = item.get("id") or item.get("name") or ""
            else:
                name = _value(item, "id", _value(item, "name", ""))
            name = str(name).strip()
            if name and name not in names:
                names.append(name)
        return names

    def _show_models(self, models: list[str], failed: bool) -> None:
        if not self.is_mounted:
            return
        option_list = self.query_one("#models-list", OptionList)
        option_list.clear_options()
        self._models = models
        model_filter = self.query_one("#models-filter", Input)
        model_filter.disabled = failed or not models
        if failed:
            self.query_one("#models-status", Static).update("Unable to load models")
        elif not models:
            self.query_one("#models-status", Static).update("No models reported")
        else:
            option_list.add_options(models)
            self.query_one("#models-status", Static).update(f"{len(models)} models")

    @on(Input.Changed, "#models-filter")
    def _filter_models(self, event: Input.Changed) -> None:
        query = event.value.strip().casefold()
        models = [model for model in self._models if query in model.casefold()]
        option_list = self.query_one("#models-list", OptionList)
        option_list.clear_options()
        option_list.add_options(models)
        self.query_one("#models-status", Static).update(
            f"{len(models)} of {len(self._models)} models"
        )

    @on(Button.Pressed, "#models-close")
    def _close(self) -> None:
        self.dismiss(None)


class RelayApp(App):
    """Mouse-first provider dashboard backed by ``RelayServer``."""

    TITLE = "Switchboard"
    ENABLE_COMMAND_PALETTE = False
    BINDINGS = [Binding("q", "quit", "Quit", show=False)]

    CSS = f"""
    Screen {{
        background: {PALETTE['background']};
        color: {PALETTE['foreground']};
    }}

    #topbar {{
        height: 3;
        padding: 0 1;
        background: #141619;
        border-bottom: solid {PALETTE['border']};
        align-vertical: middle;
    }}

    #app-title {{
        width: 18;
        text-style: bold;
        color: {PALETTE['foreground']};
    }}

    #active-provider {{
        width: 1fr;
        color: {PALETTE['secondary']};
        content-align: left middle;
    }}

    #listen-address {{
        width: auto;
        margin-right: 2;
        color: {PALETTE['muted']};
        content-align: right middle;
    }}

    #quit {{ width: 9; }}

    #main-tabs {{ height: 1fr; }}
    #main-tabs > ContentTabs {{
        height: 3;
        padding-left: 1;
        background: {PALETTE['background']};
    }}

    #main-tabs Tab {{
        height: 3;
        padding: 1 2;
        color: #777c84;
        background: {PALETTE['background']};
    }}

    #main-tabs Tab:hover {{
        color: {PALETTE['foreground']};
        background: {PALETTE['surface']};
    }}

    #main-tabs Tab.-active {{
        color: #f0f1f2;
        background: {PALETTE['surface']};
        text-style: bold;
    }}

    TabPane {{
        height: 1fr;
        padding: 1 2;
        background: {PALETTE['background']};
    }}

    Button {{
        height: 3;
        min-width: 9;
        border: solid #34383e;
        background: {PALETTE['surface']};
        color: #b9bdc3;
    }}

    Button:hover {{
        border: solid #555b64;
        background: #22252a;
        color: #f0f1f2;
    }}

    Button:focus {{ border: solid #777d86; }}

    Input {{
        height: 3;
        border: solid {PALETTE['border']};
        background: {PALETTE['background']};
        color: {PALETTE['foreground']};
    }}

    Input:focus {{ border: solid #777d86; }}

    #dashboard-metrics, #stats-metrics {{
        grid-size: 4 1;
        grid-columns: 1fr 1fr 1fr 1fr;
        grid-gutter: 0 1;
        height: 6;
    }}

    .metric {{
        height: 5;
        padding: 0 1;
        border: solid {PALETTE['border']};
        background: {PALETTE['surface']};
        content-align: left middle;
    }}

    .section-title {{
        height: 2;
        margin-top: 1;
        color: {PALETTE['secondary']};
        text-style: bold;
        content-align: left middle;
    }}

    DataTable {{
        border: solid {PALETTE['border']};
        background: #121416;
        color: #c8cbd0;
    }}

    DataTable > .datatable--header {{
        background: #1d2024;
        color: #b9bdc3;
        text-style: bold;
    }}

    DataTable > .datatable--odd-row {{ background: #15171a; }}
    DataTable > .datatable--cursor {{ background: #292d32; color: #f0f1f2; }}
    DataTable > .datatable--hover {{ background: #22252a; color: #f0f1f2; }}

    #activity {{ height: 1fr; min-height: 8; }}

    #providers-scroll {{ height: 1fr; }}
    #providers-table {{ height: 8; }}
    #keys-table {{ height: 8; }}

    #provider-actions, #key-editor, #stats-controls, #tunnel-actions {{
        height: 3;
        margin-top: 1;
    }}

    #provider-actions {{
        grid-size: 5 1;
        grid-columns: 12 12 12 12 12;
        grid-gutter: 0 1;
    }}
    #provider-actions Button {{ width: 1fr; }}
    #key-editor {{
        grid-size: 9 1;
        grid-columns: 2fr 8 2fr 9 9 9 9 9 10;
        grid-gutter: 0 1;
    }}
    #key-input, #key-rpm-input, #key-proxy-input,
    #key-editor Button {{ width: 1fr; margin: 0; }}

    #stats-controls {{
        grid-size: 6 1;
        grid-columns: 8 8 8 8 11 1fr;
        grid-gutter: 0 1;
        margin-top: 0;
    }}
    #stats-scroll {{ height: 1fr; }}
    #stats-controls Button {{ width: 1fr; min-width: 0; margin: 0; }}
    #stats-controls Button.stats-period-selected {{
        border: solid #777d86;
        background: #292d32;
        color: #f0f1f2;
        text-style: bold;
    }}
    #stats-status {{
        width: 1fr;
        color: {PALETTE['muted']};
        content-align: left middle;
    }}
    #stats-breakdown {{ height: 1fr; min-height: 8; }}

    #tunnel-status {{
        height: 4;
        padding: 0 1;
        border: solid {PALETTE['border']};
        background: {PALETTE['surface']};
        content-align: left middle;
    }}
    #tunnel-scroll {{ height: 1fr; }}
    #tunnel-actions {{
        grid-size: 10 1;
        grid-columns: 9 9 18 12 16 14 1fr 14 16 12;
        grid-gutter: 0 1;
    }}
    #tunnel-actions Button, #tunnel-provider-select,
    #tunnel-rpm-input {{ width: 1fr; min-width: 0; margin: 0; }}
    #tunnel-all-models.tunnel-all-selected {{
        border: solid #777d86;
        background: #292d32;
        color: #f0f1f2;
        text-style: bold;
    }}
    #tunnel-profile {{
        height: 3;
        grid-size: 2 1;
        grid-columns: 1fr 16;
        grid-gutter: 0 1;
    }}
    #tunnel-profile-input, #tunnel-save-profile {{ width: 1fr; min-width: 0; margin: 0; }}
    #tunnel-model-tests {{
        height: 3;
        grid-size: 3 1;
        grid-columns: 17 12 1fr;
        grid-gutter: 0 1;
    }}
    #tunnel-model-tests Button {{ width: 1fr; min-width: 0; margin: 0; }}
    #tunnel-model-test-status {{
        width: 1fr;
        color: {PALETTE['muted']};
        content-align: left middle;
    }}
    #tunnel-models {{
        height: 1fr;
        min-height: 7;
        border: solid {PALETTE['border']};
        background: #121416;
        color: #c8cbd0;
    }}
    #tunnel-models > .option-list--option-highlighted,
    #tunnel-models > .option-list--option-hover {{
        background: #292d32;
        color: #f0f1f2;
    }}
    #tunnel-models > .selection-list--button,
    #tunnel-models > .selection-list--button-highlighted {{ color: {PALETTE['muted']}; }}
    #tunnel-models > .selection-list--button-selected,
    #tunnel-models > .selection-list--button-selected-highlighted {{ color: {PALETTE['success']}; }}
    #tunnel-clients {{ height: 1fr; min-height: 6; }}
    #shared-tunnel-status {{
        height: 3;
        padding: 0 1;
        border-bottom: solid {PALETTE['border']};
        content-align: left middle;
    }}
    #shared-tunnels-table {{ height: 1fr; min-height: 8; }}
    #shared-tunnel-actions {{
        height: 3;
        grid-size: 4 1;
        grid-columns: 1fr 1fr 1fr 1fr;
        grid-gutter: 0 1;
    }}
    #shared-tunnel-actions Button {{ width: 1fr; min-width: 0; margin: 0; }}

    Screen.compact #topbar {{ padding: 0; }}
    Screen.compact #app-title {{ width: 13; }}
    Screen.compact #listen-address {{ display: none; }}
    Screen.compact #quit {{ width: 7; }}
    Screen.compact #main-tabs > ContentTabs {{ height: 2; }}
    Screen.compact #main-tabs Tab {{ height: 2; padding: 0 1; }}
    Screen.compact TabPane {{ padding: 0 1; }}

    Screen.compact #dashboard-metrics,
    Screen.compact #stats-metrics {{
        grid-size: 2 2;
        grid-columns: 1fr 1fr;
        height: 10;
    }}

    Screen.compact .metric {{ height: 5; padding: 0; }}
    Screen.compact .section-title {{ height: 1; margin-top: 0; }}
    Screen.compact #activity {{ min-height: 6; }}
    Screen.compact #stats-breakdown {{ min-height: 5; }}
    Screen.compact #providers-table {{ height: 7; }}
    Screen.compact #keys-table {{ height: 6; }}
    Screen.compact #provider-actions {{
        grid-columns: 1fr 1fr 1fr 1fr 1fr;
        grid-gutter: 0;
    }}
    Screen.compact #key-editor {{
        height: 7;
        grid-size: 6 2;
        grid-columns: 1fr 1fr 1fr 1fr 1fr 1fr;
        grid-rows: 3 3;
        grid-gutter: 1 1;
    }}
    Screen.compact #key-input,
    Screen.compact #key-rpm-input,
    Screen.compact #key-proxy-input {{ column-span: 2; }}
    Screen.compact #key-editor Button {{ min-width: 0; }}
    Screen.compact #tunnel-status {{ height: 3; padding: 0; }}
    Screen.compact #tunnel-actions {{ margin-top: 0; }}
    Screen.tunnel-narrow #tunnel-actions {{
        height: 7;
        grid-size: 5 2;
        grid-columns: 1fr 1fr 1fr 1fr 1fr;
        grid-rows: 3 3;
        grid-gutter: 1 1;
    }}
    Screen.tunnel-tiny #tunnel-actions {{
        height: 15;
        grid-size: 2 5;
        grid-columns: 1fr 1fr;
        grid-rows: 3 3 3 3 3;
    }}
    Screen.tunnel-tiny #tunnel-profile {{
        height: 7;
        grid-size: 1 2;
        grid-columns: 1fr;
        grid-rows: 3 3;
        grid-gutter: 1 0;
    }}
    Screen.tunnel-tiny #tunnel-model-tests {{
        height: 7;
        grid-size: 2 2;
        grid-columns: 1fr 1fr;
        grid-rows: 3 3;
        grid-gutter: 1 1;
    }}
    Screen.tunnel-tiny #tunnel-model-test-status {{ column-span: 2; }}
    Screen.compact #tunnel-models {{ min-height: 6; }}
    Screen.compact #tunnel-clients {{ min-height: 6; }}
    Screen.compact #shared-tunnel-status {{ height: 3; padding: 0; }}
    Screen.compact #shared-tunnels-table {{ min-height: 6; }}
    Screen.compact #shared-tunnel-actions {{
        height: 7;
        grid-size: 2 2;
        grid-columns: 1fr 1fr;
        grid-rows: 3 3;
        grid-gutter: 1 1;
    }}
    """

    def __init__(self, server: Any) -> None:
        super().__init__()
        self.register_theme(MONO_THEME)
        self.theme = MONO_THEME.name
        self.server = server
        self.listen_host, self.listen_port = server.server_address[:2]
        self._activity_rows: set[str] = set()
        self._provider_rows: set[str] = set()
        self._key_rows: set[str] = set()
        self._key_order: tuple[str, ...] = ()
        self._providers: dict[str, Any] = {}
        self._keys: dict[str, Mapping[str, Any]] = {}
        self._selected_provider_id: str | None = None
        self._selected_key_id: str | None = None
        self._stats_period = "24h"
        self._history_generation = 0
        self._tunnel_models_generation = 0
        self._tunnel_busy = False
        self._tunnel_models_loading = False
        self._tunnel_models_loaded = False
        self._tunnel_models: list[str] = []
        self._tunnel_model_probe_states: dict[str, str] = {}
        self._tunnel_model_probe_busy = False
        self._tunnel_model_probe_generation = 0
        self._tunnel_model_probe_active_generation: int | None = None
        self._tunnel_model_probe_done = 0
        self._tunnel_model_probe_total = 0
        self._tunnel_provider_id: str | None = None
        self._tunnel_provider_available = False
        self._tunnel_provider_options: tuple[tuple[str, str], ...] = ()
        self._tunnel_select_all_requested = False
        self._tunnel_client_rows: set[str] = set()
        self._tunnel_profile_available = False
        self._tunnel_url = ""
        self._shared_tunnel_rows: set[str] = set()
        self._shared_tunnels: dict[int, dict[str, str]] = {}
        self._selected_shared_tunnel_position: int | None = None
        self._shared_tunnel_revision = -1
        self._shared_tunnel_available = False
        self._own_shared_tunnel_state: str | None = None
        self._shared_tunnel_refreshing = False
        self._shared_tunnel_refresh_generation = 0
        self._shared_tunnel_busy = False

    def compose(self) -> ComposeResult:
        with Horizontal(id="topbar"):
            yield Static("Switchboard", id="app-title")
            yield Static(id="active-provider")
            yield Static(f"{self.listen_host}:{self.listen_port}", id="listen-address")
            yield Button("Quit", id="quit")

        with TabbedContent(initial="dashboard", id="main-tabs"):
            with TabPane("Dashboard", id="dashboard"):
                with Grid(id="dashboard-metrics"):
                    yield Static(id="metric-throughput", classes="metric")
                    yield Static(id="metric-outcomes", classes="metric")
                    yield Static(id="metric-latency", classes="metric")
                    yield Static(id="metric-tokens", classes="metric")
                yield Static("Activity · double-click a row for safe details", classes="section-title")
                yield ActivityTable(id="activity")

            with TabPane("Providers", id="providers"):
                with VerticalScroll(id="providers-scroll"):
                    yield DataTable(id="providers-table")
                    with Grid(id="provider-actions"):
                        yield Button("Activate", id="activate-provider")
                        yield Button("Add", id="add-provider")
                        yield Button("Edit", id="edit-provider")
                        yield Button("Delete", id="delete-provider")
                        yield Button("Models", id="provider-models")
                    yield Static("API keys · priority order", classes="section-title")
                    yield DataTable(id="keys-table")
                    with Grid(id="key-editor"):
                        yield Input(placeholder="New API key", password=True, max_length=512, id="key-input")
                        yield Input(
                            value="0",
                            placeholder="RPM",
                            type="integer",
                            restrict=r"\d*",
                            id="key-rpm-input",
                        )
                        yield Input(
                            placeholder="HTTP proxy · blank direct / keep",
                            password=True,
                            max_length=2048,
                            id="key-proxy-input",
                        )
                        yield Button("Add key", id="add-key")
                        yield Button("Save", disabled=True, id="update-key-settings")
                        yield Button("Remove", disabled=True, id="remove-key")
                        yield Button("Higher", disabled=True, id="move-key-up")
                        yield Button("Lower", disabled=True, id="move-key-down")
                        yield Button("Reset CD", disabled=True, id="reset-key-cooldown")

            with TabPane("Stats", id="stats"):
                with VerticalScroll(id="stats-scroll"):
                    with Grid(id="stats-controls"):
                        for label, value in PERIODS:
                            yield Button(
                                label,
                                id=f"stats-period-{value}",
                                classes="stats-period-selected" if value == "24h" else "",
                            )
                        yield Button("Refresh", id="refresh-stats")
                        yield Static("Load on tab entry", id="stats-status")
                    with Grid(id="stats-metrics"):
                        yield Static(id="stats-requests", classes="metric")
                        yield Static(id="stats-latency", classes="metric")
                        yield Static(id="stats-tokens", classes="metric")
                        yield Static(id="stats-retries", classes="metric")
                    yield Static("Provider / model breakdown", classes="section-title")
                    yield DataTable(id="stats-breakdown")

            with TabPane("Tunnel", id="tunnel"):
                with VerticalScroll(id="tunnel-scroll"):
                    yield Static(id="tunnel-status")
                    with Grid(id="tunnel-actions"):
                        yield Button("Start", id="tunnel-start")
                        yield Button("Stop", id="tunnel-stop")
                        yield Button("Refresh models", id="tunnel-refresh-models")
                        yield Button("Copy URL", id="tunnel-copy-url")
                        yield Button("Copy API key", id="tunnel-copy-key")
                        yield Button("Rotate key", id="tunnel-rotate-key")
                        provider_select = Select(
                            [],
                            prompt="Tunnel provider",
                            allow_blank=True,
                            compact=True,
                            disabled=True,
                            id="tunnel-provider-select",
                        )
                        provider_select.border_title = "Tunnel provider"
                        yield provider_select
                        yield Button("All models", disabled=True, id="tunnel-all-models")
                        rpm_input = Input(
                            value="0",
                            type="integer",
                            restrict=r"\d*",
                            disabled=True,
                            id="tunnel-rpm-input",
                        )
                        rpm_input.border_title = "Per-IP RPM"
                        rpm_input.border_subtitle = "0 = unlimited"
                        yield rpm_input
                        yield Button("Save RPM", disabled=True, id="tunnel-save-rpm")
                    with Grid(id="tunnel-profile"):
                        profile_input = Input(
                            password=True,
                            disabled=True,
                            id="tunnel-profile-input",
                        )
                        profile_input.border_title = "Publisher profile"
                        profile_input.border_subtitle = "masked"
                        yield profile_input
                        yield Button("Save profile", disabled=True, id="tunnel-save-profile")
                    with Grid(id="tunnel-model-tests"):
                        yield Button("Test selected", disabled=True, id="tunnel-test-selected")
                        yield Button("Test all", disabled=True, id="tunnel-test-all")
                        yield Static("Models are not tested", id="tunnel-model-test-status")
                    models = SelectionList(id="tunnel-models")
                    models.border_title = "Available models · open tab to load"
                    yield models
                    yield Static(
                        "Tunnel clients · Enter or double-click for safe activity",
                        classes="section-title",
                    )
                    clients = TunnelClientsTable(id="tunnel-clients")
                    clients.border_title = "Clients unavailable"
                    yield clients

            with TabPane("Shared", id="shared-tunnels"):
                yield Static(
                    "Open this tab to sync shared tunnels",
                    id="shared-tunnel-status",
                )
                yield DataTable(id="shared-tunnels-table")
                with Grid(id="shared-tunnel-actions"):
                    yield Button("Refresh", id="shared-tunnel-refresh")
                    yield Button("Pause", disabled=True, id="shared-tunnel-pause")
                    yield Button("Resume", disabled=True, id="shared-tunnel-resume")
                    yield Button("Stop", disabled=True, id="shared-tunnel-stop")

    def on_mount(self) -> None:
        activity = self.query_one("#activity", ActivityTable)
        for label, key, width in (
            ("State", "state", 10),
            ("Model", "model", 16),
            ("Tok/s", "token_speed", 8),
            ("Context", "context", 10),
            ("RPM", "rpm", 7),
            ("Time", "time", 8),
            ("Provider", "provider", 13),
            ("Method", "method", 7),
            ("Path", "path", 24),
            ("Status", "status", 20),
            ("Latency", "latency", 10),
            ("Queue", "queue", 9),
            ("Retries", "retries", 12),
            ("Tokens", "tokens", 10),
        ):
            activity.add_column(label, key=key, width=width)
        activity.cursor_type = "row"
        activity.zebra_stripes = True

        providers = self.query_one("#providers-table", DataTable)
        for label, key, width in (
            ("State", "state", 9),
            ("Name", "name", 16),
            ("URL", "url", 32),
            ("Auth", "auth", 12),
            ("Limit", "rpm", 10),
            ("Actual", "actual", 8),
            ("Queue", "queue", 7),
            ("Keys", "keys", 6),
            ("Cache", "cache", 7),
        ):
            providers.add_column(label, key=key, width=width)
        providers.cursor_type = "row"
        providers.zebra_stripes = True

        keys = self.query_one("#keys-table", DataTable)
        for label, key, width in (
            ("Priority", "priority", 9),
            ("Key", "label", 24),
            ("RPM", "rpm", 10),
            ("Actual", "actual", 8),
            ("Proxy", "proxy", 22),
            ("Cooldown", "cooldown", 10),
            ("429", "retries", 7),
        ):
            keys.add_column(label, key=key, width=width)
        keys.cursor_type = "row"
        keys.zebra_stripes = True

        breakdown = self.query_one("#stats-breakdown", DataTable)
        for label, key, width in (
            ("Provider", "provider", 18),
            ("Model", "model", 28),
            ("Requests", "requests", 10),
            ("Tokens", "tokens", 12),
            ("Cached", "cached", 12),
            ("Average", "average", 11),
        ):
            breakdown.add_column(label, key=key, width=width)
        breakdown.cursor_type = "row"
        breakdown.zebra_stripes = True

        shared = self.query_one("#shared-tunnels-table", DataTable)
        shared.add_column("State", key="state", width=12)
        shared.add_column("Tunnel", key="name", width=48)
        shared.cursor_type = "row"
        shared.zebra_stripes = True

        clients = self.query_one("#tunnel-clients", TunnelClientsTable)
        for label, key, width in (
            ("IP", "ip", 24),
            ("Actual RPM", "rpm", 11),
            ("Count", "connected", 7),
            ("Active", "active", 8),
            ("Queued", "queued", 8),
            ("Last seen", "last_seen", 15),
            ("State", "state", 12),
        ):
            clients.add_column(label, key=key, width=width)
        clients.cursor_type = "row"
        clients.zebra_stripes = True

        self._refresh_live()
        self.set_interval(0.25, self._refresh_live)
        self.set_interval(2.0, self._poll_shared_tunnels)

    def _refresh_live(self) -> None:
        try:
            active_widget = self.query_one("#active-provider", Static)
        except NoMatches:
            return
        self.screen.set_class(self.screen.size.width < 100 or self.screen.size.height < 30, "compact")
        self.screen.set_class(self.screen.size.width < 150, "tunnel-narrow")
        self.screen.set_class(self.screen.size.width < 80, "tunnel-tiny")
        try:
            snapshot = self.server.snapshot()
        except Exception:
            self.query_one("#active-provider", Static).update("Relay unavailable")
            return

        providers = tuple(snapshot.get("providers") or self.server.providers())
        active = next((provider for provider in providers if bool(_value(provider, "active"))), None)
        active_tab = self.query_one("#main-tabs", TabbedContent).active
        active_name = str(_value(active, "name", "No active provider"))
        active_label = Text(
            "Private model tunnel"
            if active_tab == "tunnel"
            else "Shared tunnel control"
            if active_tab == "shared-tunnels"
            else active_name
        )
        if snapshot.get("config_error"):
            active_label.append(" · settings not saved", style=PALETTE["error"])
        if snapshot.get("history_error"):
            active_label.append(" · history not recording", style=PALETTE["error"])
        active_widget.update(active_label)
        self._update_dashboard(snapshot, active)
        self._update_activity(snapshot)
        self._update_providers(providers)
        self._refresh_tunnel()
        if active_tab == "tunnel":
            self._refresh_tunnel_clients()
            self._refresh_tunnel_model_probes()
        elif active_tab == "providers":
            self._refresh_keys()

    def _tunnel_profile_locked(self, state: str) -> bool:
        return self._tunnel_busy or state.casefold() in {
            "starting",
            "reconnecting",
            "stopping",
            "active",
            "online",
            "ready",
            "running",
            "started",
        }

    def _refresh_tunnel(self) -> None:
        try:
            status = self.query_one("#tunnel-status", Static)
        except NoMatches:
            return
        try:
            snapshot = self.server.tunnel_snapshot()
            if not isinstance(snapshot, Mapping):
                raise TypeError
        except Exception:
            status.update(Text("● Tunnel unavailable", style=PALETTE["error"]))
            self._tunnel_url = ""
            self.query_one("#tunnel-provider-select", Select).disabled = True
            self.query_one("#tunnel-all-models", Button).disabled = True
            self.query_one("#tunnel-profile-input", Input).disabled = True
            self.query_one("#tunnel-save-profile", Button).disabled = True
            for button_id in (
                "#tunnel-start",
                "#tunnel-stop",
                "#tunnel-copy-url",
                "#tunnel-copy-key",
                "#tunnel-rotate-key",
            ):
                self.query_one(button_id, Button).disabled = True
            return

        state = str(snapshot.get("state") or "stopped").casefold()
        allowed_count = max(0, _integer(snapshot.get("allowed_count")))
        reported_url = str(snapshot.get("url") or "")
        claimed_running = state in {"active", "online", "ready", "running", "started"}
        self._tunnel_url = _tunnel_public_url(snapshot)
        running = bool(self._tunnel_url)
        route_available = bool(snapshot.get("route_available", True))
        paused = running and not route_available
        shared_state = (
            self._own_shared_tunnel_state if self._shared_tunnel_available else None
        )
        shared_stopped = shared_state == "stopped"
        stopping = state == "stopping"
        reconnecting = state == "reconnecting"
        failed = (
            not reconnecting
            and (
                bool(snapshot.get("error"))
                or state in {"error", "failed"}
                or bool(
                    reported_url
                    and not TUNNEL_PUBLIC_URL_PATTERN.fullmatch(reported_url)
                )
            )
        )
        starting = state == "starting" or (claimed_running and not running and not failed)
        if failed:
            color, title, detail = (
                PALETTE["error"],
                "Tunnel unavailable",
                _tunnel_error(snapshot.get("error"), "connection unavailable"),
            )
        elif reconnecting:
            color, title, detail = (
                PALETTE["waiting"],
                "Reconnecting",
                _tunnel_error(snapshot.get("error"), "retrying automatically"),
            )
        elif shared_state == "paused":
            paused = True
            color, title, detail = (
                PALETTE["waiting"],
                "Paused",
                "paused through Shared Control",
            )
        elif shared_stopped:
            color, title, detail = (
                PALETTE["muted"],
                "Stopped",
                "stopped through Shared Control",
            )
        elif paused:
            color, title, detail = (
                PALETTE["waiting"],
                "Paused",
                "active provider cannot use the public route",
            )
        elif running:
            color, title, detail = PALETTE["success"], "Online", self._tunnel_url
        elif starting:
            color, title, detail = PALETTE["live"], "Starting", "waiting for public readiness"
        elif stopping:
            color, title, detail = PALETTE["waiting"], "Stopping", "finishing connections"
        else:
            color, title, detail = PALETTE["muted"], "Offline", "VPS route stopped"
        label = Text("● ", style=color)
        label.append(title, style=PALETTE["foreground"])
        label.append(
            f"\n{allowed_count} model{'s' if allowed_count != 1 else ''} exposed · {detail}",
            style=PALETTE["muted"],
        )
        status.update(label)

        busy = self._tunnel_busy or starting or stopping
        self.query_one("#tunnel-start", Button).disabled = (
            busy or running or reconnecting or not allowed_count
        )
        self.query_one("#tunnel-stop", Button).disabled = (
            self._tunnel_busy
            or stopping
            or not (running or starting or reconnecting)
        )
        self.query_one("#tunnel-copy-url", Button).disabled = (
            not self._tunnel_url or paused or shared_stopped
        )
        self.query_one("#tunnel-copy-key", Button).disabled = self._tunnel_busy
        self.query_one("#tunnel-rotate-key", Button).disabled = self._tunnel_busy
        self.query_one("#tunnel-refresh-models", Button).disabled = (
            self._tunnel_models_loading or self._tunnel_model_probe_busy
        )
        provider_locked = self._tunnel_profile_locked(state)
        self.query_one("#tunnel-provider-select", Select).disabled = (
            provider_locked or not self._tunnel_provider_available
        )
        profile_disabled = (
            provider_locked or not self._tunnel_profile_available
        )
        self.query_one("#tunnel-profile-input", Input).disabled = profile_disabled
        self.query_one("#tunnel-save-profile", Button).disabled = profile_disabled
        self._update_all_models_control()

    def _refresh_tunnel_clients(self) -> None:
        clients = _read_tunnel_clients(self.server)
        table = self.query_one("#tunnel-clients", TunnelClientsTable)
        if clients is None:
            for stale in tuple(self._tunnel_client_rows):
                table.remove_row(stale)
                table.forget_client(stale)
            self._tunnel_client_rows.clear()
            table.border_title = "Clients unavailable · retrying automatically"
            return

        current = {str(client["key"]) for client in clients}
        for stale in self._tunnel_client_rows - current:
            table.remove_row(stale)
            table.forget_client(stale)
        for client in clients:
            row_key = str(client["key"])
            values = {
                "ip": _cell(client["ip"], 24),
                "rpm": _cell(_rpm(client["actual_rpm"]), 11),
                "connected": _cell(_count(client["connected"]), 7),
                "active": _cell(_count(client["active"]), 8),
                "queued": _cell(_count(client["queued"]), 8),
                "last_seen": _cell(client["last_seen"], 15),
                "state": _state_cell(client["state"]),
            }
            if row_key not in self._tunnel_client_rows:
                table.add_row(*(values[key] for key in values), key=row_key)
            else:
                for column, value in values.items():
                    table.update_cell(row_key, column, value)
            table.set_client(row_key, client)
        self._tunnel_client_rows = current
        table.border_title = (
            f"{len(clients)} connected client{'s' if len(clients) != 1 else ''}"
            if clients
            else "No tunnel clients"
        )

    def _poll_shared_tunnels(self) -> None:
        try:
            active = self.query_one("#main-tabs", TabbedContent).active
        except NoMatches:
            return
        if active in {"tunnel", "shared-tunnels"}:
            self._refresh_shared_tunnels()

    def _refresh_shared_tunnels(self) -> None:
        if self._shared_tunnel_refreshing or self._shared_tunnel_busy:
            return
        self._shared_tunnel_refreshing = True
        self._shared_tunnel_refresh_generation += 1
        refresh_generation = self._shared_tunnel_refresh_generation
        if not self._shared_tunnel_rows:
            try:
                self.query_one("#shared-tunnel-status", Static).update(
                    "Syncing shared tunnels…"
                )
            except NoMatches:
                # The Tunnel tab also polls the owner's shared state, but the
                # Shared Control widgets are not mounted there.
                pass
        self._update_shared_tunnel_actions()

        def load() -> None:
            try:
                snapshot = self.server.shared_tunnels()
            except Exception:
                snapshot = None
            self.app.call_from_thread(
                self._apply_shared_tunnel_refresh,
                refresh_generation,
                snapshot,
            )

        self.run_worker(
            load, thread=True, group="shared-tunnel-refresh", exclusive=True
        )

    def _apply_shared_tunnel_refresh(
        self, refresh_generation: int, snapshot: Any
    ) -> None:
        if refresh_generation != self._shared_tunnel_refresh_generation:
            return
        self._shared_tunnel_refreshing = False
        self._apply_shared_tunnels(snapshot)

    def _apply_shared_tunnels(self, snapshot: Any) -> None:
        self._shared_tunnel_refreshing = False
        if not self.is_mounted:
            return
        try:
            self.query_one("#shared-tunnels-table", DataTable)
            shared_status = self.query_one("#shared-tunnel-status", Static)
        except NoMatches:
            shared_status = None
        if not isinstance(snapshot, Mapping) or snapshot.get("available") is not True:
            self._shared_tunnel_available = False
            self._own_shared_tunnel_state = None
            self._replace_shared_tunnel_rows({})
            if shared_status is not None:
                label = Text("● ", style=PALETTE["error"])
                label.append("Shared control unavailable", style=PALETTE["foreground"])
                label.append(" · retrying automatically", style=PALETTE["muted"])
                shared_status.update(label)
            self._update_shared_tunnel_actions()
            self._refresh_tunnel()
            return

        revision = snapshot.get("revision")
        if type(revision) is not int or not 0 <= revision <= 2**63 - 1:
            self._apply_shared_tunnels(None)
            return
        if revision < self._shared_tunnel_revision:
            self._update_shared_tunnel_actions()
            return
        raw_tunnels = snapshot.get("tunnels")
        if not isinstance(raw_tunnels, (list, tuple)):
            self._apply_shared_tunnels(None)
            return

        tunnels: dict[int, dict[str, str]] = {}
        names: set[str] = set()
        for position, raw in enumerate(raw_tunnels):
            if not isinstance(raw, Mapping):
                self._apply_shared_tunnels(None)
                return
            name = str(raw.get("name") or "").strip()
            state = str(raw.get("state") or "").casefold()
            if (
                type(raw.get("position")) is not int
                or raw["position"] != position
                or SHARED_TUNNEL_NAME_PATTERN.fullmatch(name) is None
                or name in names
                or state not in {"running", "paused", "stopped"}
            ):
                self._apply_shared_tunnels(None)
                return
            names.add(name)
            tunnels[position] = {"name": name, "state": state}

        self._shared_tunnel_revision = revision
        self._shared_tunnel_available = True
        self._own_shared_tunnel_state = next(
            (
                tunnel["state"]
                for tunnel in tunnels.values()
                if tunnel["name"] == SHARED_TUNNEL_SELF_NAME
            ),
            None,
        )
        self._replace_shared_tunnel_rows(tunnels)
        if shared_status is not None:
            label = Text("● ", style=PALETTE["success"])
            label.append("Synced", style=PALETTE["foreground"])
            label.append(
                f" · {len(tunnels)} tunnel{'s' if len(tunnels) != 1 else ''} · every 2s",
                style=PALETTE["muted"],
            )
            shared_status.update(label)
        self._update_shared_tunnel_actions()
        self._refresh_tunnel()

    def _replace_shared_tunnel_rows(
        self, tunnels: dict[int, dict[str, str]]
    ) -> None:
        current = {str(position) for position in tunnels}
        self._shared_tunnels = tunnels
        if self._selected_shared_tunnel_position not in tunnels:
            self._selected_shared_tunnel_position = next(iter(tunnels), None)
        try:
            table = self.query_one("#shared-tunnels-table", DataTable)
        except NoMatches:
            self._shared_tunnel_rows.clear()
            return
        for stale in self._shared_tunnel_rows - current:
            table.remove_row(stale)
        for position, tunnel in tunnels.items():
            row_key = str(position)
            values = {
                "state": _shared_tunnel_state_cell(tunnel["state"]),
                "name": _cell(tunnel["name"], 64),
            }
            if row_key not in self._shared_tunnel_rows:
                table.add_row(values["state"], values["name"], key=row_key)
            else:
                for column, value in values.items():
                    table.update_cell(row_key, column, value)
        self._shared_tunnel_rows = current
        if self._selected_shared_tunnel_position is not None and table.row_count:
            row = table.get_row_index(str(self._selected_shared_tunnel_position))
            if table.cursor_row != row:
                table.move_cursor(row=row, column=0, animate=False, scroll=False)

    def _update_shared_tunnel_actions(self) -> None:
        try:
            refresh = self.query_one("#shared-tunnel-refresh", Button)
            pause = self.query_one("#shared-tunnel-pause", Button)
            resume = self.query_one("#shared-tunnel-resume", Button)
            stop = self.query_one("#shared-tunnel-stop", Button)
        except NoMatches:
            return
        selected = self._shared_tunnels.get(self._selected_shared_tunnel_position)
        state = selected["state"] if selected else ""
        locked = (
            not self._shared_tunnel_available
            or self._shared_tunnel_busy
        )
        refresh.disabled = self._shared_tunnel_busy
        pause.disabled = locked or state != "running"
        resume.disabled = locked or state not in {"paused", "stopped"}
        stop.disabled = locked or state not in {"running", "paused"}

    def _control_shared_tunnel(self, action: str) -> None:
        position = self._selected_shared_tunnel_position
        tunnel = self._shared_tunnels.get(position)
        allowed_states = {
            "pause": {"running"},
            "resume": {"paused", "stopped"},
        }.get(action)
        if (
            action not in {"pause", "resume", "stop"}
            or position is None
            or tunnel is None
            or self._shared_tunnel_busy
            or (allowed_states and tunnel["state"] not in allowed_states)
            or (action == "stop" and tunnel["state"] == "stopped")
        ):
            return

        revision = self._shared_tunnel_revision
        self._shared_tunnel_busy = True
        self._shared_tunnel_refresh_generation += 1
        self._shared_tunnel_refreshing = False
        self.query_one("#shared-tunnel-status", Static).update(
            f"Applying {action} to {tunnel['name']}…"
        )
        self._update_shared_tunnel_actions()

        def control() -> None:
            try:
                snapshot = self.server.control_shared_tunnel(
                    position, revision, action
                )
            except Exception:
                snapshot = None
            self.app.call_from_thread(
                self._finish_shared_tunnel_control, action, snapshot
            )

        self.run_worker(
            control, thread=True, group="shared-tunnel-control", exclusive=True
        )

    def _finish_shared_tunnel_control(self, action: str, snapshot: Any) -> None:
        self._shared_tunnel_busy = False
        if not self.is_mounted:
            return
        if not isinstance(snapshot, Mapping) or snapshot.get("available") is not True:
            self.notify(
                "Shared tunnel operation failed",
                severity="error",
                timeout=3,
                markup=False,
            )
            self._update_shared_tunnel_actions()
            return
        self._apply_shared_tunnels(snapshot)
        self.notify(
            {
                "pause": "Tunnel paused",
                "resume": "Tunnel resumed",
                "stop": "Tunnel stopped",
            }[action],
            timeout=2,
            markup=False,
        )

    def _load_tunnel_provider(self) -> None:
        control = self.query_one("#tunnel-provider-select", Select)
        options = tuple(
            (str(_value(provider, "name", provider_id)), provider_id)
            for provider_id, provider in self._providers.items()
        )
        getter = getattr(self.server, "tunnel_provider_id", None)
        setter = getattr(self.server, "set_tunnel_provider", None)
        supported = callable(getter) and callable(setter)
        try:
            provider_id = str(getter() or "") if supported else ""
        except Exception:
            provider_id = ""
            supported = False
        if not supported:
            provider_id = next(
                (
                    item_id
                    for item_id, provider in self._providers.items()
                    if bool(_value(provider, "active"))
                ),
                "",
            )
        if provider_id not in self._providers:
            provider_id = ""

        changed = bool(self._tunnel_provider_id and provider_id != self._tunnel_provider_id)
        self._tunnel_provider_id = provider_id or None
        self._tunnel_provider_available = supported and bool(options) and bool(provider_id)
        with self.prevent(Select.Changed):
            if options != self._tunnel_provider_options:
                control.set_options(options)
                self._tunnel_provider_options = options
            value = provider_id if provider_id else Select.NULL
            if control.value != value:
                control.value = value
        if changed:
            self._tunnel_models_generation += 1
            self._tunnel_models_loading = False
            self._tunnel_models_loaded = False
            self._tunnel_models.clear()
            self._invalidate_tunnel_model_probes()
            model_list = self.query_one("#tunnel-models", SelectionList)
            model_list.clear_options()
            model_list.disabled = True
            model_list.border_title = "Loading selected provider catalog…"
        self._update_all_models_control()
        if changed and self.query_one("#main-tabs", TabbedContent).active == "tunnel":
            self._load_tunnel_models()

    def _select_all_tunnel_models(self) -> None:
        if self._tunnel_models_loading:
            self._tunnel_select_all_requested = True
            return
        if not self._tunnel_models_loaded:
            self._tunnel_select_all_requested = True
            self._load_tunnel_models()
            return
        provider_id = self._tunnel_provider_id
        if not provider_id:
            return
        model_list = self.query_one("#tunnel-models", SelectionList)
        models = list(self._tunnel_models)
        selected = {str(model) for model in model_list.selected}
        allowed = () if set(models) <= selected else tuple(models)
        try:
            current_provider = getattr(self.server, "tunnel_provider_id", None)
            if callable(current_provider) and str(current_provider()) != provider_id:
                return
            saved = self.server.set_tunnel_allowed_models(allowed)
            source = allowed if saved is None else saved
            allowed = tuple(
                model for model in (str(item).strip() for item in source) if model
            )
        except Exception:
            self._apply_tunnel_models(
                self._tunnel_models_generation,
                provider_id,
                models,
                (),
                False,
                True,
            )
            return
        self._apply_tunnel_models(
            self._tunnel_models_generation,
            provider_id,
            models,
            allowed,
            False,
            False,
        )

    def _load_tunnel_models(self) -> None:
        if self._tunnel_models_loading:
            return
        model_list = self.query_one("#tunnel-models", SelectionList)
        provider_id = self._tunnel_provider_id
        if not provider_id:
            model_list.border_title = "Model catalog unavailable"
            self._update_all_models_control()
            return

        self._tunnel_models_loading = True
        self._tunnel_models_generation += 1
        generation = self._tunnel_models_generation
        model_list.disabled = True
        self.query_one("#tunnel-refresh-models", Button).disabled = True
        model_list.border_title = "Loading model catalog…"

        def load() -> None:
            try:
                fetch = getattr(self.server, "fetch_tunnel_models", None)
                raw_models = fetch() if callable(fetch) else self.server.fetch_models(provider_id)
                models = ModelsScreen._model_names(raw_models)
                allowed_source = self.server.tunnel_allowed_models()
                allowed = tuple(
                    model for model in (str(item).strip() for item in allowed_source) if model
                )
            except Exception:
                self.app.call_from_thread(
                    self._apply_tunnel_models,
                    generation,
                    provider_id,
                    [],
                    (),
                    False,
                    True,
                )
            else:
                self.app.call_from_thread(
                    self._apply_tunnel_models,
                    generation,
                    provider_id,
                    models,
                    allowed,
                    False,
                    False,
                )

        self.run_worker(load, thread=True, group="tunnel-models", exclusive=True)

    def _load_tunnel_rpm(self) -> None:
        rpm_input = self.query_one("#tunnel-rpm-input", Input)
        save = self.query_one("#tunnel-save-rpm", Button)
        try:
            rpm = int(self.server.tunnel_rpm_per_ip())
            if rpm < 0:
                raise ValueError
        except Exception:
            rpm_input.disabled = save.disabled = True
            return
        rpm_input.value = str(rpm)
        rpm_input.disabled = save.disabled = False

    def _save_tunnel_rpm(self) -> None:
        rpm_input = self.query_one("#tunnel-rpm-input", Input)
        try:
            rpm = int(rpm_input.value or "0")
            if rpm < 0:
                raise ValueError
        except ValueError:
            self.notify(
                "Per-IP RPM must be 0 or greater",
                severity="error",
                timeout=3,
                markup=False,
            )
            rpm_input.focus()
            return
        try:
            self.server.set_tunnel_rpm_per_ip(rpm)
        except Exception:
            self.notify(
                "Unable to save rate limit",
                severity="error",
                timeout=3,
                markup=False,
            )
            return
        rpm_input.value = str(rpm)
        self.notify(
            "Per-IP RPM: unlimited" if rpm == 0 else "Per-IP RPM saved",
            timeout=2,
            markup=False,
        )

    def _load_tunnel_profile(self) -> None:
        profile_input = self.query_one("#tunnel-profile-input", Input)
        save = self.query_one("#tunnel-save-profile", Button)
        profile_input.disabled = save.disabled = True
        self._tunnel_profile_available = False
        try:
            profile = self.server.tunnel_publisher_profile()
            if profile is None:
                profile = ""
            if not isinstance(profile, str):
                raise ValueError
            snapshot = self.server.tunnel_snapshot()
            if not isinstance(snapshot, Mapping):
                raise TypeError
            state = str(snapshot.get("state") or "stopped").casefold()
        except Exception:
            return
        profile_input.value = profile
        self._tunnel_profile_available = True
        locked = self._tunnel_profile_locked(state)
        profile_input.disabled = save.disabled = locked

    def _save_tunnel_profile(self) -> None:
        profile_input = self.query_one("#tunnel-profile-input", Input)
        try:
            self.server.set_tunnel_publisher_profile(profile_input.value)
        except Exception:
            self.notify(
                "Unable to save publisher profile",
                severity="error",
                timeout=3,
                markup=False,
            )
            return
        self._refresh_tunnel()
        self.notify("Publisher profile saved", timeout=2, markup=False)

    def _apply_tunnel_models(
        self,
        generation: int,
        provider_id: str,
        models: list[str],
        allowed: tuple[str, ...],
        select_all: bool,
        failed: bool,
    ) -> None:
        if (
            generation != self._tunnel_models_generation
            or provider_id != self._tunnel_provider_id
            or not self.is_mounted
        ):
            return
        if select_all and not failed:
            try:
                current_provider = getattr(self.server, "tunnel_provider_id", None)
                if callable(current_provider) and str(current_provider()) != provider_id:
                    return
                saved = self.server.set_tunnel_allowed_models(tuple(models))
                source = models if saved is None else saved
                allowed = tuple(
                    model for model in (str(item).strip() for item in source) if model
                )
            except Exception:
                failed = True
        self._tunnel_models_loading = False
        model_list = self.query_one("#tunnel-models", SelectionList)
        model_list.disabled = False
        self.query_one("#tunnel-refresh-models", Button).disabled = False
        if failed:
            self._tunnel_select_all_requested = False
            model_list.border_title = "Unable to refresh model catalog"
            self._update_all_models_control()
            return

        self._tunnel_models_loaded = True
        self._tunnel_models = list(dict.fromkeys((*models, *allowed)))
        self._tunnel_model_probe_states = {
            model: state
            for model, state in self._tunnel_model_probe_states.items()
            if model in self._tunnel_models and state in TUNNEL_MODEL_PROBE_STATES
        }
        selected = set(allowed)
        model_list.clear_options()
        model_list.add_options(
            (
                _tunnel_model_prompt(
                    model, self._tunnel_model_probe_states.get(model, "")
                ),
                model,
                model in selected,
            )
            for model in self._tunnel_models
        )
        model_list.border_title = (
            "No models reported"
            if not self._tunnel_models
            else f"{len(selected)} of {len(self._tunnel_models)} models exposed"
        )
        self._update_all_models_control()
        self._refresh_tunnel_model_probes()
        if self._tunnel_select_all_requested:
            self._tunnel_select_all_requested = False
            self._select_all_tunnel_models()

    def _update_all_models_control(self) -> None:
        try:
            button = self.query_one("#tunnel-all-models", Button)
            model_list = self.query_one("#tunnel-models", SelectionList)
        except NoMatches:
            return
        selected = {str(model) for model in model_list.selected}
        all_selected = bool(self._tunnel_models) and set(self._tunnel_models) <= selected
        button.label = "All models ✓" if all_selected else "All models"
        button.set_class(all_selected, "tunnel-all-selected")
        button.disabled = (
            self._tunnel_models_loading or not self._tunnel_provider_id
        )

    def _invalidate_tunnel_model_probes(self) -> None:
        """Forget results for the previous provider without racing its workers."""
        self._tunnel_model_probe_generation += 1
        self._tunnel_model_probe_states.clear()
        self._tunnel_model_probe_done = 0
        self._tunnel_model_probe_total = 0
        self._tunnel_model_probe_busy = (
            self._tunnel_model_probe_active_generation is not None
        )
        self._update_tunnel_model_probe_controls()

    def _tunnel_probe_provider_is_current(self, provider_id: str) -> bool:
        if not provider_id or provider_id != self._tunnel_provider_id:
            return False
        getter = getattr(self.server, "tunnel_provider_id", None)
        if not callable(getter):
            return True
        try:
            return str(getter() or "") == provider_id
        except Exception:
            return False

    def _refresh_tunnel_model_probes(self) -> None:
        """Read the backend's bounded, RAM-only probe snapshot."""
        provider_id = self._tunnel_provider_id
        generation = self._tunnel_models_generation
        getter = getattr(self.server, "tunnel_model_probes", None)
        if not provider_id or not callable(getter):
            self._update_tunnel_model_probe_controls()
            return
        try:
            snapshot = getter()
        except Exception:
            self._update_tunnel_model_probe_controls()
            return
        if (
            generation != self._tunnel_models_generation
            or not self._tunnel_probe_provider_is_current(provider_id)
        ):
            return

        states: dict[str, str] = {}
        if isinstance(snapshot, (list, tuple)):
            for item in snapshot:
                if not isinstance(item, Mapping):
                    continue
                model = str(item.get("model") or "")
                state = str(item.get("state") or "").casefold()
                if model in self._tunnel_models and state in TUNNEL_MODEL_PROBE_STATES:
                    states[model] = state
        if self._tunnel_model_probe_busy:
            self._tunnel_model_probe_states.update(states)
        else:
            self._tunnel_model_probe_states = states
        self._update_tunnel_model_probe_controls()

    def _update_tunnel_model_probe_controls(self) -> None:
        try:
            model_list = self.query_one("#tunnel-models", SelectionList)
            selected_button = self.query_one("#tunnel-test-selected", Button)
            all_button = self.query_one("#tunnel-test-all", Button)
            status = self.query_one("#tunnel-model-test-status", Static)
        except NoMatches:
            return

        for index, model in enumerate(self._tunnel_models):
            if index >= model_list.option_count:
                break
            model_list.replace_option_prompt_at_index(
                index,
                _tunnel_model_prompt(
                    model, self._tunnel_model_probe_states.get(model, "")
                ),
            )

        probe = getattr(self.server, "probe_tunnel_model", None)
        supported = callable(probe) and bool(self._tunnel_provider_id)
        selected = {str(model) for model in model_list.selected}
        locked = self._tunnel_model_probe_busy or self._tunnel_models_loading
        selected_button.disabled = locked or not supported or not selected
        all_button.disabled = locked or not supported or not self._tunnel_models

        if self._tunnel_model_probe_busy:
            if self._tunnel_model_probe_total:
                status.update(
                    f"Testing {self._tunnel_model_probe_done}/{self._tunnel_model_probe_total}"
                    " · max 4 concurrent"
                )
            else:
                status.update("Finishing previous model tests…")
            return
        counts = {
            state: sum(
                value == state for value in self._tunnel_model_probe_states.values()
            )
            for state in ("testing", "available", "unavailable", "timeout")
        }
        labels = (
            ("testing", "testing"),
            ("available", "available"),
            ("unavailable", "unavailable"),
            ("timeout", "timeout"),
        )
        summary = [f"{counts[state]} {label}" for state, label in labels if counts[state]]
        status.update(" · ".join(summary) if summary else "Models are not tested")

    def _test_tunnel_models(self, *, all_models: bool) -> None:
        if self._tunnel_model_probe_busy or self._tunnel_models_loading:
            return
        provider_id = self._tunnel_provider_id
        model_list = self.query_one("#tunnel-models", SelectionList)
        selected = {str(model) for model in model_list.selected}
        models = tuple(
            model
            for model in self._tunnel_models
            if all_models or model in selected
        )
        probe = getattr(self.server, "probe_tunnel_model", None)
        if not provider_id or not callable(probe) or not models:
            self._update_tunnel_model_probe_controls()
            return

        self._tunnel_model_probe_generation += 1
        probe_generation = self._tunnel_model_probe_generation
        catalog_generation = self._tunnel_models_generation
        self._tunnel_model_probe_active_generation = probe_generation
        self._tunnel_model_probe_busy = True
        self._tunnel_model_probe_done = 0
        self._tunnel_model_probe_total = len(models)
        for model in models:
            self._tunnel_model_probe_states[model] = "testing"
        self._update_tunnel_model_probe_controls()
        self.query_one("#tunnel-refresh-models", Button).disabled = True

        def run() -> None:
            def run_one(model: str) -> tuple[str, Any]:
                try:
                    return model, probe(model)
                except Exception:
                    return model, {"model": model, "state": "unavailable"}

            try:
                with ThreadPoolExecutor(
                    max_workers=min(4, len(models)),
                    thread_name_prefix="tunnel-model-test",
                ) as pool:
                    futures = [pool.submit(run_one, model) for model in models]
                    for future in as_completed(futures):
                        model, result = future.result()
                        try:
                            self.app.call_from_thread(
                                self._apply_tunnel_model_probe_result,
                                probe_generation,
                                catalog_generation,
                                provider_id,
                                model,
                                result,
                            )
                        except RuntimeError:
                            return
            except Exception:
                pass
            finally:
                try:
                    self.app.call_from_thread(
                        self._finish_tunnel_model_probe_batch,
                        probe_generation,
                        catalog_generation,
                        provider_id,
                    )
                except RuntimeError:
                    pass

        try:
            self.run_worker(
                run, thread=True, group="tunnel-model-probes", exclusive=True
            )
        except Exception:
            self._finish_tunnel_model_probe_batch(
                probe_generation, catalog_generation, provider_id
            )

    def _apply_tunnel_model_probe_result(
        self,
        probe_generation: int,
        catalog_generation: int,
        provider_id: str,
        model: str,
        result: Any,
    ) -> None:
        if (
            probe_generation != self._tunnel_model_probe_generation
            or probe_generation != self._tunnel_model_probe_active_generation
            or catalog_generation != self._tunnel_models_generation
            or not self._tunnel_probe_provider_is_current(provider_id)
            or model not in self._tunnel_models
        ):
            return
        returned_model = str(_value(result, "model", ""))
        state = str(_value(result, "state", "")).casefold()
        if returned_model != model or state not in TUNNEL_MODEL_PROBE_STATES:
            state = "unavailable"
        self._tunnel_model_probe_states[model] = state
        self._tunnel_model_probe_done = min(
            self._tunnel_model_probe_total,
            self._tunnel_model_probe_done + 1,
        )
        self._update_tunnel_model_probe_controls()

    def _finish_tunnel_model_probe_batch(
        self,
        probe_generation: int,
        catalog_generation: int,
        provider_id: str,
    ) -> None:
        if probe_generation != self._tunnel_model_probe_active_generation:
            return
        self._tunnel_model_probe_active_generation = None
        self._tunnel_model_probe_busy = False
        current = (
            probe_generation == self._tunnel_model_probe_generation
            and catalog_generation == self._tunnel_models_generation
            and self._tunnel_probe_provider_is_current(provider_id)
        )
        if current:
            self._tunnel_model_probe_done = self._tunnel_model_probe_total
            self._refresh_tunnel_model_probes()
        else:
            self._tunnel_model_probe_done = 0
            self._tunnel_model_probe_total = 0
            self._update_tunnel_model_probe_controls()
        self.query_one("#tunnel-refresh-models", Button).disabled = (
            self._tunnel_models_loading
        )

    def _run_tunnel_operation(self, action: str) -> None:
        if self._tunnel_busy:
            return
        self._tunnel_busy = True
        self._refresh_tunnel()

        def run() -> None:
            try:
                if action == "start":
                    self.server.start_tunnel()
                elif action == "stop":
                    self.server.stop_tunnel()
                else:
                    self.server.rotate_tunnel_token()
            except Exception as error:
                detail = _operation_error(error)
                try:
                    detail = _tunnel_error(
                        self.server.tunnel_snapshot().get("error"), detail
                    )
                except Exception:
                    pass
                self.app.call_from_thread(
                    self._finish_tunnel_operation, action, False, detail
                )
            else:
                self.app.call_from_thread(
                    self._finish_tunnel_operation, action, True, ""
                )

        self.run_worker(run, thread=True, group="tunnel-operation", exclusive=True)

    def _finish_tunnel_operation(
        self, action: str, succeeded: bool, detail: str = ""
    ) -> None:
        self._tunnel_busy = False
        if not self.is_mounted:
            return
        self._refresh_tunnel()
        if succeeded:
            self.notify(
                {
                    "start": "Tunnel online" if self._tunnel_url else "Tunnel is starting",
                    "stop": "Tunnel stopped",
                    "rotate": "API key rotated",
                }[action],
                timeout=2,
                markup=False,
            )
        else:
            self.notify(
                detail or "Tunnel operation failed",
                title="Tunnel operation failed",
                severity="error",
                timeout=3,
                markup=False,
            )

    def _copy_tunnel_value(self, api_key: bool) -> None:
        try:
            value = self.server.tunnel_access_token() if api_key else self._tunnel_url
            copy = getattr(self, "copy_to_clipboard", None)
            if not value or not callable(copy):
                raise RuntimeError
            copy(str(value))
        except Exception:
            self.notify("Unable to copy", severity="error", timeout=3, markup=False)
        else:
            self.notify(
                "API key copied" if api_key else "Tunnel URL copied",
                timeout=2,
                markup=False,
            )

    def _update_dashboard(self, snapshot: Mapping[str, Any], active: Any) -> None:
        actual_rpm = snapshot.get("actual_rpm", _value(active, "actual_rpm", 0))
        configured_rpm = snapshot.get("rpm", _value(active, "rpm", 0))
        active_requests = _integer(snapshot.get("active"))
        live = snapshot.get("live")
        if isinstance(live, (list, tuple)):
            queued = sum(_value(event, "state") == "queued" for event in live)
            retrying = sum(_value(event, "state") == "retry" for event in live)
        else:
            queued = _integer(snapshot.get("queued", _value(active, "queued", 0)))
            retrying = 0
        self.query_one("#metric-throughput", Static).update(
            _metric(
                "Throughput",
                f"{_rpm(actual_rpm)} actual RPM",
                f"Limit {_rpm(configured_rpm, unlimited=True)} · {active_requests} active · {queued} queued · {retrying} retrying",
            )
        )
        self.query_one("#metric-outcomes", Static).update(
            _metric(
                "Outcomes",
                f"{_count(snapshot.get('successes'))} success · {_count(snapshot.get('errors'))} error",
                f"{_count(snapshot.get('retries'))} retries · {_count(snapshot.get('retries_429'))} from 429",
            )
        )
        self.query_one("#metric-latency", Static).update(
            _metric(
                "Latency",
                f"{_milliseconds(snapshot.get('average_ms'))} average",
                f"{_milliseconds(snapshot.get('p95_ms'))} p95",
            )
        )
        tokens = self._token_totals(snapshot)
        self.query_one("#metric-tokens", Static).update(
            _metric(
                "Tokens",
                f"{_count(tokens['total_tokens'])} total · {_count(tokens['cached_tokens'])} cached",
                f"{_count(tokens['context_tokens'])} context · {_count(tokens['output_tokens'])} out · {_count(tokens['reasoning_tokens'])} reasoning",
            )
        )

    @staticmethod
    def _token_totals(snapshot: Mapping[str, Any]) -> dict[str, int]:
        if any(field in snapshot for field in TOKEN_FIELDS):
            return {field: _integer(snapshot.get(field)) for field in TOKEN_FIELDS}
        completed = snapshot.get("recent") or ()
        return {
            field: sum(_integer(event.get(field)) for event in completed if isinstance(event, Mapping))
            for field in TOKEN_FIELDS
        }

    def _update_activity(self, snapshot: Mapping[str, Any]) -> None:
        table = self.query_one("#activity", ActivityTable)
        provider_rpm = snapshot.get("provider_actual_rpm")
        if not isinstance(provider_rpm, Mapping):
            provider_rpm = {}
        events_by_id: dict[str, Mapping[str, Any]] = {}
        for event in (*(snapshot.get("recent") or ()), *(snapshot.get("live") or ())):
            if not isinstance(event, Mapping):
                continue
            identity = event.get("_request_id", event.get("seq"))
            if identity is None:
                continue
            events_by_id[f"request:{identity}"] = event
        events = list(events_by_id.items())[-80:]
        current_keys = {key for key, _ in events}
        follow = table.scroll_y >= table.max_scroll_y

        for stale in self._activity_rows - current_keys:
            table.remove_row(stale)
            table.forget_detail(stale)
        added = False
        for row_key, event in events:
            values = {
                "state": _state_cell(event.get("state")),
                "model": _cell(event.get("model"), 16),
                "token_speed": _cell(_token_speed(event.get("tokens_per_second")), 8),
                "context": _cell(_count(event.get("context_tokens")), 10),
                "rpm": _cell(
                    _rpm(
                        provider_rpm.get(
                            event.get("provider_id"), snapshot.get("actual_rpm", 0)
                        )
                    ),
                    7,
                ),
                "time": _cell(event.get("time"), 8),
                "provider": _cell(event.get("provider"), 13),
                "method": _cell(event.get("method"), 7),
                "path": _cell(event.get("path"), 24),
                "status": _http_status_cell(event.get("status")),
                "latency": _cell(_milliseconds(event.get("latency_ms")), 10),
                "queue": _cell(_milliseconds(event.get("queue_ms")), 9),
                "retries": _cell(
                    f"{_integer(event.get('retries'))} / 429:{_integer(event.get('retries_429'))}", 12
                ),
                "tokens": _cell(_count(event.get("total_tokens")), 10),
            }
            if row_key not in self._activity_rows:
                table.add_row(*(values[key] for key in values), key=row_key)
                added = True
            else:
                for column_key, value in values.items():
                    table.update_cell(row_key, column_key, value)
            table.set_detail(row_key, _safe_event_detail(event))
        self._activity_rows = current_keys
        if added and follow and table.row_count:
            table.move_cursor(row=table.row_count - 1, column=0, animate=False)

    def _update_providers(self, providers: tuple[Any, ...]) -> None:
        table = self.query_one("#providers-table", DataTable)
        self._providers = {str(_value(provider, "id")): provider for provider in providers}
        active_provider_id = next(
            (
                provider_id
                for provider_id, provider in self._providers.items()
                if bool(_value(provider, "active"))
            ),
            None,
        )
        current_keys = set(self._providers)
        for stale in self._provider_rows - current_keys:
            table.remove_row(stale)
        for provider_id, provider in self._providers.items():
            queued = _integer(_value(provider, "queued"))
            cooldown = _number(_value(provider, "cooldown_ms"))
            state = (
                "Active"
                if bool(_value(provider, "active"))
                else "Retrying"
                if queued and cooldown
                else "Queued"
                if queued
                else "Ready"
            )
            values = {
                "state": _cell(state, 9),
                "name": _cell(_value(provider, "name"), 16),
                "url": _cell(_value(provider, "upstream"), 32),
                "auth": _cell(_value(provider, "auth_mode"), 12),
                "rpm": _cell(
                    _rpm(
                        _value(provider, "effective_rpm", _value(provider, "rpm")),
                        unlimited=True,
                    ),
                    10,
                ),
                "actual": _cell(_rpm(_value(provider, "actual_rpm", 0)), 8),
                "queue": _cell(queued, 7),
                "keys": _cell(_value(provider, "key_count", 0), 6),
                "cache": _cell("1h" if bool(_value(provider, "cache_1h")) else "Off", 7),
            }
            if provider_id not in self._provider_rows:
                table.add_row(*(values[key] for key in values), key=provider_id)
            else:
                for column_key, value in values.items():
                    table.update_cell(provider_id, column_key, value)
        self._provider_rows = current_keys

        if self._selected_provider_id not in current_keys:
            self._selected_provider_id = active_provider_id or next(iter(current_keys), None)
            self._selected_key_id = None
        if self._selected_provider_id and table.row_count:
            row = table.get_row_index(self._selected_provider_id)
            if table.cursor_row != row:
                table.move_cursor(row=row, column=0, animate=False, scroll=False)
        self._load_tunnel_provider()

    def _refresh_keys(self) -> None:
        provider_id = self._selected_provider_id
        if not provider_id:
            self._keys = {}
            self._key_order = ()
            self._update_key_actions()
            return
        try:
            rows = tuple(self.server.provider_keys(provider_id))
        except Exception:
            rows = ()
        table = self.query_one("#keys-table", DataTable)
        self._keys = {str(_value(row, "id")): row for row in rows}
        order = tuple(self._keys)
        order_changed = order != self._key_order
        if order_changed:
            table.clear()
            self._key_rows.clear()
            self._key_order = order
        current_keys = set(self._keys)
        for stale in self._key_rows - current_keys:
            table.remove_row(stale)
        for priority, (fingerprint, row) in enumerate(self._keys.items(), 1):
            values = {
                "priority": _cell("Primary" if priority == 1 else priority, 9),
                "label": _cell(_value(row, "label", fingerprint), 24),
                "rpm": _cell(_rpm(_value(row, "rpm"), unlimited=True), 10),
                "actual": _cell(_rpm(_value(row, "actual_rpm", 0)), 8),
                "proxy": _cell(_value(row, "proxy", "Direct"), 22),
                "cooldown": _cell(_milliseconds(_value(row, "cooldown_ms")), 10),
                "retries": _cell(_value(row, "retries_429", 0), 7),
            }
            if fingerprint not in self._key_rows:
                table.add_row(*(values[key] for key in values), key=fingerprint)
            else:
                for column_key, value in values.items():
                    table.update_cell(fingerprint, column_key, value)
        self._key_rows = current_keys
        selection_changed = self._selected_key_id not in current_keys
        if selection_changed:
            self._selected_key_id = next(iter(current_keys), None)
            if self._selected_key_id:
                rpm = _integer(_value(self._keys[self._selected_key_id], "rpm", 0))
                self.query_one("#key-rpm-input", Input).value = str(rpm)
                self.query_one("#key-proxy-input", Input).value = ""
        if self._selected_key_id and table.row_count and (order_changed or selection_changed):
            row = table.get_row_index(self._selected_key_id)
            if table.cursor_row != row:
                table.move_cursor(row=row, column=0, animate=False, scroll=False)
        self._update_key_actions()

    def _update_key_actions(self) -> None:
        selected = self._keys.get(self._selected_key_id or "")
        pinned = bool(_value(selected, "pinned", False)) if selected else False
        self.query_one("#add-key", Button).disabled = not self._selected_provider_id
        for button_id in ("#update-key-settings", "#remove-key"):
            self.query_one(button_id, Button).disabled = selected is None or pinned
        self.query_one("#move-key-up", Button).disabled = (
            selected is None or pinned or not bool(_value(selected, "can_move_up", False))
        )
        self.query_one("#move-key-down", Button).disabled = (
            selected is None or pinned or not bool(_value(selected, "can_move_down", False))
        )
        self.query_one("#reset-key-cooldown", Button).disabled = selected is None

    @on(ActivityTable.DetailRequested)
    def _show_activity_detail(self, event: ActivityTable.DetailRequested) -> None:
        self.push_screen(EventDetailScreen(event.detail))

    @on(TunnelClientsTable.ClientRequested)
    def _show_tunnel_client(self, event: TunnelClientsTable.ClientRequested) -> None:
        self.push_screen(TunnelClientScreen(self.server, event.client))

    @on(DataTable.RowHighlighted, "#providers-table")
    def _provider_highlighted(self, event: DataTable.RowHighlighted) -> None:
        provider_id = event.row_key.value
        if provider_id is not None and str(provider_id) in self._providers:
            self._selected_provider_id = str(provider_id)
            self._selected_key_id = None
            self._refresh_keys()

    @on(DataTable.RowHighlighted, "#keys-table")
    def _key_highlighted(self, event: DataTable.RowHighlighted) -> None:
        fingerprint = event.row_key.value
        if fingerprint is None or str(fingerprint) not in self._keys:
            return
        fingerprint = str(fingerprint)
        if fingerprint == self._selected_key_id:
            self._update_key_actions()
            return
        self._selected_key_id = fingerprint
        rpm = _integer(_value(self._keys[self._selected_key_id], "rpm", 0))
        self.query_one("#key-rpm-input", Input).value = str(rpm)
        self.query_one("#key-proxy-input", Input).value = ""
        self._update_key_actions()

    @on(DataTable.RowHighlighted, "#shared-tunnels-table")
    def _shared_tunnel_highlighted(self, event: DataTable.RowHighlighted) -> None:
        position = str(event.row_key.value or "")
        if position.isdecimal() and int(position) in self._shared_tunnels:
            self._selected_shared_tunnel_position = int(position)
            self._update_shared_tunnel_actions()

    @on(TabbedContent.TabActivated, "#main-tabs")
    def _tab_activated(self, event: TabbedContent.TabActivated) -> None:
        if event.pane.id == "providers":
            self._refresh_keys()
        elif event.pane.id == "stats":
            self._load_stats()
        elif event.pane.id == "tunnel":
            self.query_one("#active-provider", Static).update("Private model tunnel")
            self._load_tunnel_provider()
            self._load_tunnel_rpm()
            self._load_tunnel_profile()
            self._refresh_tunnel()
            self._refresh_tunnel_clients()
            self._refresh_tunnel_model_probes()
            self._refresh_shared_tunnels()
            if not self._tunnel_models_loaded:
                self._load_tunnel_models()
        elif event.pane.id == "shared-tunnels":
            self.query_one("#active-provider", Static).update(
                "Shared tunnel control"
            )
            self._refresh_shared_tunnels()
        else:
            self._refresh_live()

    @on(SelectionList.SelectionToggled, "#tunnel-models")
    def _tunnel_selection_changed(
        self, event: SelectionList.SelectionToggled
    ) -> None:
        selected = tuple(str(model) for model in event.selection_list.selected)
        try:
            self.server.set_tunnel_allowed_models(selected)
        except Exception:
            self.notify(
                "Unable to save model access",
                severity="error",
                timeout=3,
                markup=False,
            )
            self._tunnel_models_loaded = False
            self._load_tunnel_models()
            return
        event.selection_list.border_title = (
            f"{len(selected)} of {event.selection_list.option_count} models exposed"
        )
        self._update_all_models_control()
        self._update_tunnel_model_probe_controls()
        self._refresh_tunnel()

    @on(Select.Changed, "#tunnel-provider-select")
    def _tunnel_provider_changed(self, event: Select.Changed) -> None:
        if event.value is Select.NULL:
            return
        provider_id = str(event.value)
        if provider_id == self._tunnel_provider_id or provider_id not in self._providers:
            return
        previous = self._tunnel_provider_id
        try:
            selected = self.server.set_tunnel_provider(provider_id)
        except Exception as error:
            with self.prevent(Select.Changed):
                event.select.value = previous if previous else Select.NULL
            self._notify_error(error)
            return
        selected_id = str(selected or provider_id)
        self._tunnel_provider_id = selected_id if selected_id in self._providers else provider_id
        self._tunnel_models_generation += 1
        self._tunnel_models_loading = False
        self._tunnel_select_all_requested = False
        self._tunnel_models_loaded = False
        self._tunnel_models.clear()
        self._invalidate_tunnel_model_probes()
        model_list = self.query_one("#tunnel-models", SelectionList)
        model_list.clear_options()
        model_list.border_title = "Loading selected provider catalog…"
        self._update_all_models_control()
        self._load_tunnel_models()
        self._refresh_tunnel()
        name = str(_value(self._providers[self._tunnel_provider_id], "name", provider_id))
        self.notify(f"Tunnel provider: {name}", timeout=2, markup=False)

    def _set_stats_period(self, period: str) -> None:
        if period not in {value for _, value in PERIODS}:
            return
        self._stats_period = period
        for _label, value in PERIODS:
            self.query_one(f"#stats-period-{value}", Button).set_class(
                value == period, "stats-period-selected"
            )
        if self.query_one("#main-tabs", TabbedContent).active == "stats":
            self._load_stats()

    @on(Button.Pressed)
    def _button_pressed(self, event: Button.Pressed) -> None:
        button_id = event.button.id
        if button_id == "quit":
            self.exit()
        elif button_id == "activate-provider":
            self._activate_provider()
        elif button_id == "add-provider":
            self.push_screen(ProviderFormScreen(), self._provider_added)
        elif button_id == "edit-provider":
            self._open_provider_edit()
        elif button_id == "delete-provider":
            self._open_provider_delete()
        elif button_id == "provider-models":
            self._open_models()
        elif button_id == "add-key":
            self._add_key()
        elif button_id == "update-key-settings":
            self._update_key_settings()
        elif button_id == "remove-key":
            self._remove_key()
        elif button_id == "move-key-up":
            self._move_key(-1)
        elif button_id == "move-key-down":
            self._move_key(1)
        elif button_id == "reset-key-cooldown":
            self._reset_key_cooldown()
        elif button_id and button_id.startswith("stats-period-"):
            self._set_stats_period(button_id.removeprefix("stats-period-"))
        elif button_id == "refresh-stats":
            self._load_stats()
        elif button_id == "tunnel-start":
            self._run_tunnel_operation("start")
        elif button_id == "tunnel-stop":
            self._run_tunnel_operation("stop")
        elif button_id == "tunnel-refresh-models":
            self._load_tunnel_models()
        elif button_id == "tunnel-all-models":
            self._select_all_tunnel_models()
        elif button_id == "tunnel-test-selected":
            self._test_tunnel_models(all_models=False)
        elif button_id == "tunnel-test-all":
            self._test_tunnel_models(all_models=True)
        elif button_id == "tunnel-copy-url":
            self._copy_tunnel_value(False)
        elif button_id == "tunnel-copy-key":
            self._copy_tunnel_value(True)
        elif button_id == "tunnel-rotate-key":
            self._run_tunnel_operation("rotate")
        elif button_id == "tunnel-save-rpm":
            self._save_tunnel_rpm()
        elif button_id == "tunnel-save-profile":
            self._save_tunnel_profile()
        elif button_id == "shared-tunnel-refresh":
            self._refresh_shared_tunnels()
        elif button_id == "shared-tunnel-pause":
            self._control_shared_tunnel("pause")
        elif button_id == "shared-tunnel-resume":
            self._control_shared_tunnel("resume")
        elif button_id == "shared-tunnel-stop":
            self._control_shared_tunnel("stop")

    def _notify_error(self, error: Exception) -> None:
        self.notify(_operation_error(error), title="Unable to apply change", severity="error", markup=False)

    def _activate_provider(self) -> None:
        if not self._selected_provider_id:
            return
        try:
            self.server.select(self._selected_provider_id)
        except Exception as error:
            self._notify_error(error)
        else:
            self._tunnel_models_loaded = False
        self._refresh_live()

    def _provider_added(self, values: dict[str, Any] | None) -> None:
        if not values:
            return
        try:
            provider = self.server.add_provider(**values)
        except Exception as error:
            self._notify_error(error)
            return
        self._selected_provider_id = str(_value(provider, "id", "")) or None
        self._selected_key_id = None
        self._refresh_live()

    def _open_provider_edit(self) -> None:
        provider_id = self._selected_provider_id
        provider = self._providers.get(provider_id or "")
        if not provider_id or provider is None:
            return
        self.push_screen(
            ProviderFormScreen(provider),
            lambda values: self._provider_edited(provider_id, values),
        )

    def _provider_edited(self, provider_id: str, values: dict[str, Any] | None) -> None:
        if not values:
            return
        try:
            self.server.update_provider(provider_id, **values)
        except Exception as error:
            self._notify_error(error)
            return
        self._refresh_live()

    def _open_provider_delete(self) -> None:
        provider_id = self._selected_provider_id
        provider = self._providers.get(provider_id or "")
        if not provider_id or provider is None:
            return
        name = str(_value(provider, "name", "provider"))
        self.push_screen(
            ConfirmDeleteScreen(name),
            lambda confirmed: self._provider_deleted(provider_id, bool(confirmed)),
        )

    def _provider_deleted(self, provider_id: str, confirmed: bool) -> None:
        if not confirmed:
            return
        try:
            active = self.server.delete_provider(provider_id)
        except Exception as error:
            self._notify_error(error)
            return
        self._selected_provider_id = str(_value(active, "id", "")) or None
        self._selected_key_id = None
        self._refresh_live()

    def _open_models(self) -> None:
        provider_id = self._selected_provider_id
        provider = self._providers.get(provider_id or "")
        if not provider_id or provider is None:
            return
        self.push_screen(ModelsScreen(self.server, provider_id, str(_value(provider, "name", provider_id))))

    def _key_rpm(self) -> int | None:
        value = self.query_one("#key-rpm-input", Input).value
        try:
            rpm = int(value or "0")
            if rpm < 0:
                raise ValueError
        except ValueError:
            self.notify("Key RPM must be 0 or greater", severity="error", markup=False)
            return None
        return rpm

    def _key_proxy(self) -> str | None:
        value = self.query_one("#key-proxy-input", Input).value.strip()
        return "" if value.casefold() == "direct" else value or None

    def _add_key(self) -> None:
        provider_id = self._selected_provider_id
        key_input = self.query_one("#key-input", Input)
        api_key = key_input.value.strip()
        rpm = self._key_rpm()
        proxy_input = self.query_one("#key-proxy-input", Input)
        proxy_url = self._key_proxy() or ""
        if not provider_id or rpm is None:
            return
        if not api_key:
            self.notify("Enter an API key", severity="error", markup=False)
            key_input.focus()
            return
        try:
            fingerprint = self.server.add_provider_key(
                provider_id, api_key, rpm=rpm, proxy_url=proxy_url
            )
        except Exception as error:
            self._notify_error(error)
            return
        key_input.value = ""
        proxy_input.value = ""
        self._selected_key_id = str(fingerprint)
        self._refresh_keys()
        self.notify("API key added", timeout=2, markup=False)

    def _update_key_settings(self) -> None:
        provider_id = self._selected_provider_id
        fingerprint = self._selected_key_id
        rpm = self._key_rpm()
        proxy_url = self._key_proxy()
        if not provider_id or not fingerprint or rpm is None:
            return
        try:
            self.server.update_provider_key(
                provider_id, fingerprint, rpm, proxy_url=proxy_url
            )
        except Exception as error:
            self._notify_error(error)
            return
        self._refresh_keys()
        self.query_one("#key-proxy-input", Input).value = ""
        self.notify("Key settings updated", timeout=2, markup=False)

    def _remove_key(self) -> None:
        provider_id = self._selected_provider_id
        fingerprint = self._selected_key_id
        if not provider_id or not fingerprint:
            return
        try:
            self.server.remove_provider_key(provider_id, fingerprint)
        except Exception as error:
            self._notify_error(error)
            return
        self._selected_key_id = None
        self._refresh_keys()
        self.notify("API key removed", timeout=2, markup=False)

    def _move_key(self, direction: int) -> None:
        provider_id = self._selected_provider_id
        fingerprint = self._selected_key_id
        if not provider_id or not fingerprint:
            return
        try:
            self.server.move_provider_key(provider_id, fingerprint, direction)
        except Exception as error:
            self._notify_error(error)
            return
        self._refresh_keys()
        self.notify(
            "Key priority updated",
            timeout=2,
            markup=False,
        )

    def _reset_key_cooldown(self) -> None:
        provider_id = self._selected_provider_id
        fingerprint = self._selected_key_id
        if not provider_id or not fingerprint:
            return
        try:
            self.server.reset_provider_key_cooldown(provider_id, fingerprint)
        except Exception as error:
            self._notify_error(error)
            return
        self._refresh_keys()
        self.notify("Key cooldown cleared", timeout=2, markup=False)

    def _load_stats(self) -> None:
        period = self._stats_period
        self._history_generation += 1
        generation = self._history_generation
        self.query_one("#stats-status", Static).update("Loading…")

        def load() -> None:
            try:
                stats = self.server.history_stats(period)
                breakdown = self.server.history_breakdown(period)
            except Exception:
                self.app.call_from_thread(self._apply_stats, generation, period, {}, [], True)
            else:
                self.app.call_from_thread(self._apply_stats, generation, period, stats or {}, breakdown or [], False)

        self.run_worker(load, thread=True, group="history", exclusive=True)

    def _apply_stats(
        self,
        generation: int,
        period: str,
        stats: Mapping[str, Any],
        breakdown: list[Mapping[str, Any]],
        failed: bool,
    ) -> None:
        if generation != self._history_generation or not self.is_mounted:
            return
        self.query_one("#stats-status", Static).update(
            "History unavailable" if failed else f"Period {period} · {_count(stats.get('requests'))} requests"
        )
        self.query_one("#stats-requests", Static).update(
            _metric(
                "Requests",
                f"{_count(stats.get('requests'))} total",
                f"{_count(stats.get('successes'))} success · {_count(stats.get('errors'))} error · {_count(stats.get('cancelled'))} cancelled",
            )
        )
        self.query_one("#stats-latency", Static).update(
            _metric(
                "Latency",
                f"{_milliseconds(stats.get('average_ms'))} average",
                f"{_milliseconds(stats.get('p95_ms'))} p95",
            )
        )
        self.query_one("#stats-tokens", Static).update(
            _metric(
                "Tokens",
                f"{_count(stats.get('total_tokens'))} total · {_count(stats.get('cached_tokens'))} cached",
                f"{_count(stats.get('context_tokens'))} context · {_count(stats.get('output_tokens'))} out · {_count(stats.get('reasoning_tokens'))} reasoning",
            )
        )
        self.query_one("#stats-retries", Static).update(
            _metric(
                "Retries",
                f"{_count(stats.get('retries'))} total",
                f"{_count(stats.get('retries_429'))} from 429",
            )
        )

        table = self.query_one("#stats-breakdown", DataTable)
        table.clear()
        for index, row in enumerate(breakdown):
            table.add_row(
                _cell(row.get("provider"), 18),
                _cell(row.get("model"), 28),
                _cell(_count(row.get("requests")), 10),
                _cell(_count(row.get("total_tokens")), 12),
                _cell(_count(row.get("cached_tokens")), 12),
                _cell(_milliseconds(row.get("average_ms")), 11),
                key=f"stats:{index}",
            )

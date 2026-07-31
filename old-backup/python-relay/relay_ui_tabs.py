"""Top-level workflow panes for Provider Switchboard."""

from __future__ import annotations

from textual.app import ComposeResult
from textual.containers import Grid, VerticalScroll
from textual.widgets import (
    Button,
    DataTable,
    Input,
    SelectionList,
    Select,
    Static,
    TabPane,
)

from relay_ui_widgets import ActivityTable, TunnelClientsTable


class OverviewPane(TabPane):
    def __init__(self) -> None:
        super().__init__("Overview", id="dashboard")

    def compose(self) -> ComposeResult:
        with VerticalScroll(classes="pane-scroll"):
            yield Static(
                "Relay health, real load and non-overlapping token usage",
                id="overview-summary",
                classes="pane-intro",
            )
            with Grid(id="dashboard-metrics"):
                yield Static(id="metric-throughput", classes="metric")
                yield Static(id="metric-outcomes", classes="metric")
                yield Static(id="metric-latency", classes="metric")
                yield Static(id="metric-tokens", classes="metric")
            yield Static("Provider health", classes="section-title")
            yield DataTable(id="overview-providers")


class RequestsPane(TabPane):
    def __init__(self) -> None:
        super().__init__("Activity", id="requests")

    def compose(self) -> ComposeResult:
        yield Static(
            "Live and recent requests · Enter or double-click for full safe details",
            id="requests-summary",
            classes="pane-intro",
        )
        yield ActivityTable(id="activity")


class ProvidersPane(TabPane):
    def __init__(self) -> None:
        super().__init__("Providers", id="providers")

    def compose(self) -> ComposeResult:
        with VerticalScroll(classes="pane-scroll", id="providers-scroll"):
            yield Static(
                "Upstream endpoints and global provider limits",
                classes="pane-intro",
            )
            yield DataTable(id="providers-table")
            with Grid(id="provider-actions"):
                yield Button("Activate", id="activate-provider")
                yield Button("Add", id="add-provider")
                yield Button("Edit", id="edit-provider")
                yield Button("Delete", id="delete-provider")
                yield Button("Catalog", id="provider-models")


class KeysPane(TabPane):
    def __init__(self) -> None:
        super().__init__("Keys", id="keys")

    def compose(self) -> ComposeResult:
        with VerticalScroll(classes="pane-scroll", id="keys-scroll"):
            with Grid(id="key-provider-bar"):
                provider = Select(
                    [],
                    prompt="Choose provider",
                    allow_blank=True,
                    compact=True,
                    id="key-provider-select",
                )
                provider.border_title = "Provider"
                yield provider
                yield Static("Priority, RPM, proxy and cooldown", id="key-provider-status")
            yield DataTable(id="keys-table")
            with Grid(id="key-editor"):
                yield Input(
                    placeholder="New API key",
                    password=True,
                    max_length=512,
                    id="key-input",
                )
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
                yield Button("Add", id="add-key")
                yield Button("Save", disabled=True, id="update-key-settings")
                yield Button("Remove", disabled=True, id="remove-key")
                yield Button("Higher", disabled=True, id="move-key-up")
                yield Button("Lower", disabled=True, id="move-key-down")
                yield Button("Reset CD", disabled=True, id="reset-key-cooldown")


class RoutingPane(TabPane):
    def __init__(self) -> None:
        super().__init__("Routes", id="routing")

    def compose(self) -> ComposeResult:
        with VerticalScroll(classes="pane-scroll", id="routing-scroll"):
            yield Static(
                "Choose target, provider and models. Relay and Tunnel assignments are independent.",
                id="routing-status",
                classes="pane-intro",
            )
            with Grid(id="routing-controls"):
                provider = Select(
                    [],
                    prompt="Choose provider",
                    allow_blank=True,
                    compact=True,
                    disabled=True,
                    id="tunnel-provider-select",
                )
                provider.border_title = "Model provider"
                yield provider
                yield Button("Target: Tunnel", id="model-route-scope")
                yield Button("Refresh", id="tunnel-refresh-models")
                yield Button("Select all", disabled=True, id="tunnel-all-models")
            with Grid(id="routing-tests"):
                yield Button("Test selected", disabled=True, id="tunnel-test-selected")
                yield Button("Test all", disabled=True, id="tunnel-test-all")
                yield Static("Models are not tested", id="tunnel-model-test-status")
            with Grid(id="routing-workspace"):
                models = SelectionList(id="tunnel-models")
                models.border_title = "Provider catalog · open Routes to load"
                yield models
                routes = DataTable(id="routing-summary")
                routes.border_title = "Current assignments"
                yield routes


class TunnelPane(TabPane):
    def __init__(self) -> None:
        super().__init__("Tunnel", id="tunnel")

    def compose(self) -> ComposeResult:
        with VerticalScroll(classes="pane-scroll", id="tunnel-scroll"):
            yield Static(id="tunnel-status")
            yield Static(
                "Published models are managed in Routes → Target: Tunnel",
                id="tunnel-route-note",
                classes="pane-intro",
            )
            yield Static("Lifecycle and public access", classes="section-title")
            with Grid(id="tunnel-actions"):
                yield Button("Start", id="tunnel-start")
                yield Button("Stop", id="tunnel-stop")
                yield Button("Copy URL", id="tunnel-copy-url")
                yield Button("Copy API key", id="tunnel-copy-key")
                yield Button("Rotate key", id="tunnel-rotate-key")
            yield Static("Public limits", classes="section-title")
            with Grid(id="tunnel-limits"):
                rpm = Input(
                    value="0",
                    type="integer",
                    restrict=r"\d*",
                    disabled=True,
                    id="tunnel-rpm-input",
                )
                rpm.border_title = "Per-IP RPM"
                rpm.border_subtitle = "0 = unlimited"
                yield rpm
                yield Button("Save RPM", disabled=True, id="tunnel-save-rpm")
                context = Input(
                    value="0",
                    type="integer",
                    restrict=r"\d*",
                    disabled=True,
                    id="tunnel-context-input",
                )
                context.border_title = "Context KiB"
                context.border_subtitle = "0 = unlimited"
                yield context
                yield Button("Save context", disabled=True, id="tunnel-save-context")
            yield Static("Owner profile", classes="section-title")
            with Grid(id="tunnel-profile"):
                profile = Input(password=True, disabled=True, id="tunnel-profile-input")
                profile.border_title = "Publisher profile"
                profile.border_subtitle = "masked"
                yield profile
                yield Button("Save profile", disabled=True, id="tunnel-save-profile")


class ClientsPane(TabPane):
    def __init__(self) -> None:
        super().__init__("Clients", id="clients")

    def compose(self) -> ComposeResult:
        yield Static(
            "Tunnel users and real-time load · Enter or double-click for safe logs",
            id="clients-summary",
            classes="pane-intro",
        )
        clients = TunnelClientsTable(id="tunnel-clients")
        clients.border_title = "Clients unavailable"
        yield clients


class StatsPane(TabPane):
    def __init__(self) -> None:
        super().__init__("Stats", id="stats")

    def compose(self) -> ComposeResult:
        with VerticalScroll(classes="pane-scroll", id="stats-scroll"):
            with Grid(id="stats-controls"):
                for label, value in (("24h", "24h"), ("48h", "48h"), ("72h", "72h"), ("All", "all")):
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
            yield Static("Provider and model usage", classes="section-title")
            yield DataTable(id="stats-breakdown")


class SharedPane(TabPane):
    def __init__(self) -> None:
        super().__init__("Shared", id="shared-tunnels")

    def compose(self) -> ComposeResult:
        yield Static("Open this tab to sync shared tunnels", id="shared-tunnel-status")
        yield DataTable(id="shared-tunnels-table")
        with Grid(id="shared-tunnel-actions"):
            yield Button("Refresh", id="shared-tunnel-refresh")
            yield Button("Pause", disabled=True, id="shared-tunnel-pause")
            yield Button("Resume", disabled=True, id="shared-tunnel-resume")
            yield Button("Stop", disabled=True, id="shared-tunnel-stop")

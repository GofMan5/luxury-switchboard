"""Reusable mouse/keyboard tables for the Textual interface."""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any

from textual import events
from textual.binding import Binding
from textual.message import Message
from textual.widgets import DataTable


class ActivityTable(DataTable):
    """Request table which opens sanitized details on double-click or Enter."""

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
        if event.chain < 2 or not self.row_count:
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
        def __init__(
            self, table: "TunnelClientsTable", client: Mapping[str, Any]
        ) -> None:
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
        if event.chain < 2 or not self.row_count:
            return
        coordinate = self.hover_coordinate
        if not self.is_valid_coordinate(coordinate):
            coordinate = self.cursor_coordinate
        if self._request_client(coordinate):
            event.stop()

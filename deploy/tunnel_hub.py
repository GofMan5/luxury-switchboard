#!/usr/bin/env python3
"""Shared tunnel state, SSH forced command, and Caddy auth gate."""

from __future__ import annotations

import argparse
import contextlib
import json
import os
import re
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

try:  # Linux deployment; the fallback keeps local Windows tests useful.
    import fcntl
except ImportError:  # pragma: no cover - exercised on Windows instead
    fcntl = None


VERSION = 1
STATE_VERSION = 2
MAX_STATE_BYTES = 1 << 20
MAX_RESPONSE_BYTES = 64 << 10
MAX_TUNNELS = 256
STATES = frozenset({"running", "paused", "stopped"})
ID_RE = re.compile(r"[a-z0-9][a-z0-9_-]{0,47}\Z", re.ASCII)
NAME_RE = re.compile(r"Tunnel [1-9][0-9]{0,3}\Z", re.ASCII)
OWNER_RE = re.compile(r"[a-z0-9][a-z0-9._-]{0,31}\Z", re.ASCII)
ACTION_RE = re.compile(
    r"v1 (pause|resume|stop) ([0-9]{1,19}) ([0-9]{1,3})\Z", re.ASCII
)
FAILURE = b'{"v":1,"ok":false,"error":"request_failed"}\n'
UNAVAILABLE = b'{"error":"tunnel_unavailable"}\n'

_LOCKS_GUARD = threading.Lock()
_LOCKS: dict[str, threading.Lock] = {}


class HubError(Exception):
    pass


def _thread_lock(path: Path) -> threading.Lock:
    key = str(path.resolve())
    with _LOCKS_GUARD:
        return _LOCKS.setdefault(key, threading.Lock())


@contextlib.contextmanager
def _state_lock(path: Path, exclusive: bool):
    lock_path = path.with_name(path.name + ".lock")
    with _thread_lock(lock_path):
        with lock_path.open("a+b") as handle:
            if fcntl is not None:
                mode = fcntl.LOCK_EX if exclusive else fcntl.LOCK_SH
                fcntl.flock(handle.fileno(), mode)
            try:
                yield
            finally:
                if fcntl is not None:
                    fcntl.flock(handle.fileno(), fcntl.LOCK_UN)


def _valid_name(value: object) -> bool:
    return isinstance(value, str) and NAME_RE.fullmatch(value) is not None


def _validate_state(value: object) -> dict:
    if not isinstance(value, dict) or set(value) != {"v", "revision", "tunnels"}:
        raise HubError("invalid state")
    if (
        type(value["v"]) is not int
        or value["v"] not in {VERSION, STATE_VERSION}
        or type(value["revision"]) is not int
    ):
        raise HubError("invalid state")
    if (
        not 0 <= value["revision"] <= (2**63 - 1)
        or not isinstance(value["tunnels"], list)
        or len(value["tunnels"]) > MAX_TUNNELS
    ):
        raise HubError("invalid state")

    seen: set[str] = set()
    owners: set[str] = set()
    clean = []
    for tunnel in value["tunnels"]:
        expected = (
            {"id", "name", "state"}
            if value["v"] == VERSION
            else {"id", "owner", "state"}
        )
        if not isinstance(tunnel, dict) or set(tunnel) != expected:
            raise HubError("invalid state")
        tunnel_id = tunnel["id"]
        owner = None if value["v"] == VERSION else tunnel["owner"]
        if (
            not isinstance(tunnel_id, str)
            or ID_RE.fullmatch(tunnel_id) is None
            or tunnel_id in seen
            or (value["v"] == VERSION and not _valid_name(tunnel["name"]))
            or (
                owner is not None
                and (
                    not isinstance(owner, str)
                    or OWNER_RE.fullmatch(owner) is None
                    or owner in owners
                )
            )
            or not isinstance(tunnel["state"], str)
            or tunnel["state"] not in STATES
        ):
            raise HubError("invalid state")
        seen.add(tunnel_id)
        if owner is not None:
            owners.add(owner)
        clean.append({"id": tunnel_id, "owner": owner, "state": tunnel["state"]})
    return {"v": STATE_VERSION, "revision": value["revision"], "tunnels": clean}


def _read_state(path: Path) -> tuple[dict, bool]:
    try:
        with path.open("rb") as handle:
            raw = handle.read(MAX_STATE_BYTES + 1)
        if len(raw) > MAX_STATE_BYTES:
            raise HubError("state too large")
        value = json.loads(raw.decode("utf-8"))
        migrated = isinstance(value, dict) and value.get("v") == VERSION
        return _validate_state(value), migrated
    except HubError:
        raise
    except (OSError, UnicodeError, json.JSONDecodeError, TypeError, ValueError) as error:
        raise HubError("state unavailable") from error


def _write_state(path: Path, state: dict) -> None:
    raw = (json.dumps(state, ensure_ascii=False, separators=(",", ":")) + "\n").encode(
        "utf-8"
    )
    descriptor, temporary = tempfile.mkstemp(prefix=".tunnels-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as handle:
            handle.write(raw)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, 0o640)
        os.replace(temporary, path)
        if os.name == "posix":
            directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def _owner_view(state: dict, owner: str) -> tuple[list[dict], list[dict]]:
    owned = [tunnel for tunnel in state["tunnels"] if tunnel["owner"] == owner]
    others = sorted(
        (tunnel for tunnel in state["tunnels"] if tunnel["owner"] != owner),
        key=lambda tunnel: tunnel["id"],
    )
    ordered = [*owned, *others]
    visible = []
    other_index = 0
    for tunnel in ordered:
        if tunnel["owner"] == owner:
            name = "Ваш коннект"
        else:
            other_index += 1
            name = f"Tunnel {other_index}"
        visible.append({"name": name, "state": tunnel["state"]})
    return ordered, visible


def _success(state: dict, owner: str) -> bytes:
    _ordered, visible = _owner_view(state, owner)
    result = {
        "v": VERSION,
        "ok": True,
        "revision": state["revision"],
        "tunnels": visible,
    }
    raw = (json.dumps(result, ensure_ascii=False, separators=(",", ":")) + "\n").encode(
        "utf-8"
    )
    if len(raw) > MAX_RESPONSE_BYTES:
        raise HubError("response too large")
    return raw


def control(command: str, state_path: Path, owner: str) -> bytes:
    if not isinstance(owner, str) or OWNER_RE.fullmatch(owner) is None:
        raise HubError("invalid owner")
    if command == "v1 list":
        with _state_lock(state_path, exclusive=True):
            state, migrated = _read_state(state_path)
            raw = _success(state, owner)
            if migrated:
                _write_state(state_path, state)
            return raw

    match = ACTION_RE.fullmatch(command)
    if match is None:
        raise HubError("invalid command")
    action, revision_text, position_text = match.groups()
    desired = {"pause": "paused", "resume": "running", "stop": "stopped"}[action]
    with _state_lock(state_path, exclusive=True):
        state, migrated = _read_state(state_path)
        ordered, _visible = _owner_view(state, owner)
        position = int(position_text)
        if int(revision_text) != state["revision"] or position >= len(ordered):
            raise HubError("stale view")
        target = ordered[position]
        if target["state"] != desired:
            if state["revision"] == 2**63 - 1:
                raise HubError("revision exhausted")
            target["state"] = desired
            state["revision"] += 1
            raw = _success(state, owner)
            _write_state(state_path, state)
            return raw
        if migrated:
            _write_state(state_path, state)
        return _success(state, owner)


def _is_running(state_path: Path, tunnel_id: str) -> bool:
    if ID_RE.fullmatch(tunnel_id) is None:
        return False
    try:
        state, _migrated = _read_state(state_path)
    except HubError:
        return False
    return any(
        tunnel["id"] == tunnel_id and tunnel["state"] == "running"
        for tunnel in state["tunnels"]
    )


class GateServer(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, address, state_path: Path):
        self.state_path = state_path
        super().__init__(address, GateHandler)


class GateHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "tunnel-gate"
    sys_version = ""

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        match = re.fullmatch(
            r"/authorize/([a-z0-9][a-z0-9_-]{0,47})", self.path, re.ASCII
        )
        allowed = bool(match) and _is_running(self.server.state_path, match.group(1))
        body = b"" if allowed else UNAVAILABLE
        self.send_response(204 if allowed else 503)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        if body:
            self.wfile.write(body)

    def log_message(self, _format: str, *args) -> None:
        return


def _control_main(args: argparse.Namespace) -> int:
    try:
        if OWNER_RE.fullmatch(args.owner) is None:
            raise HubError("invalid owner")
        raw = control(
            os.environ.get("SSH_ORIGINAL_COMMAND", ""),
            Path(args.state),
            args.owner,
        )
        result = 0
    except Exception:
        raw = FAILURE
        result = 1
    sys.stdout.buffer.write(raw)
    sys.stdout.buffer.flush()
    return result


def _serve_main(args: argparse.Namespace) -> int:
    server = GateServer((args.listen, args.port), Path(args.state))
    try:
        server.serve_forever(poll_interval=0.2)
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument(
        "--state",
        default=os.environ.get("TUNNEL_HUB_STATE", "/var/lib/tunnel-hub/tunnels.json"),
    )
    commands = parser.add_subparsers(dest="mode", required=True)
    control_parser = commands.add_parser("control", add_help=False)
    control_parser.add_argument("--owner", required=True)
    control_parser.set_defaults(run=_control_main)
    serve_parser = commands.add_parser("serve", add_help=False)
    serve_parser.add_argument("--listen", default="172.20.0.1")
    serve_parser.add_argument("--port", type=int, default=18080)
    serve_parser.set_defaults(run=_serve_main)
    args = parser.parse_args(argv)
    if args.mode == "serve" and not 1 <= args.port <= 65535:
        return 2
    return args.run(args)


if __name__ == "__main__":
    raise SystemExit(main())

"""Provider-neutral public tunnel boundary and Windows OpenSSH lifecycle."""

from __future__ import annotations

import base64
import hmac
import http.client
import ipaddress
import json
import os
import re
import secrets
import select
import socket
import subprocess
import threading
import time
import unicodedata
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Callable, Iterable
from urllib.parse import quote, quote_plus, unquote, unquote_plus, urlsplit

from relay_runtime import ClientDisconnected, RateGate, RelayStopping


MAX_BODY_BYTES = 64 * 1024 * 1024
MAX_EVENT_BYTES = 4 * 1024 * 1024
MAX_MODELS_BYTES = 128 * 1024
READINESS_HEADER = "X-Provider-Switch-Readiness"
CLIENT_IP_HEADER = "X-Tunnel-Client-IP"
SSH_HOST = "81.90.28.122"
SSH_DESTINATION = f"model-tunnel@{SSH_HOST}"
CONTROL_SSH_DESTINATION = f"tunnel-control@{SSH_HOST}"
SSH_HOST_KEY = (
    "ssh-ed25519 "
    "AAAAC3NzaC1lZDI1NTE5AAAAIBRmk/YVgGOza+PkmT91qaX7D1vTF3YhVOlBzRnxAsEy"
)
SSH_KNOWN_HOSTS_NAME = "model-tunnel_known_hosts"
CONTROL_SSH_IDENTITY_NAME = "model-tunnel_control_ed25519"
PUBLISHER_URL_PREFIX = "https://luxuryprivate.duckdns.org/model-tunnel"
DEFAULT_PUBLISHER_PROFILE = (
    "v1.20000.0e7618d23d7e3a0e70faec6e95c792204f89f23c1da0bf75"
)
PUBLISHER_PROFILE = re.compile(r"v1\.([0-9]{5})\.([0-9a-f]{48})\Z", re.ASCII)
CONTROL_TUNNEL_POSITION = re.compile(r"(?:0|[1-9][0-9]{0,2})\Z", re.ASCII)
CONTROL_TUNNEL_NAME = re.compile(r"Tunnel [1-9][0-9]{0,3}\Z", re.ASCII)
CONTROL_SELF_NAME = "Ваш коннект"
CONTROL_ACTIONS = frozenset({"pause", "resume", "stop"})
CONTROL_OUTPUT_BYTES = 64 * 1024
CONTROL_TIMEOUT = 8.0
CONTROL_ERROR = "Shared tunnel control unavailable"
MAX_RATE_CLIENTS = 4096
RATE_CLIENT_IDLE_SECONDS = 600.0
CLIENT_READ_TIMEOUT = 30.0
CLIENT_WRITE_TIMEOUT = 2.0
READINESS_INITIAL_DELAY = 0.0
READINESS_PROBE_TIMEOUT = 8.0
PUBLIC_ROUTES = frozenset(
    {
        "/v1/responses",
        "/v1/chat/completions",
        "/v1/completions",
        "/v1/messages",
    }
)
SAFE_REQUEST_HEADERS = {
    "anthropic-version": "Anthropic-Version",
    "anthropic-beta": "Anthropic-Beta",
    "openai-beta": "OpenAI-Beta",
    "openai-version": "OpenAI-Version",
}
FREEFORM_RESPONSE_FIELDS = frozenset(
    {
        "annotations",
        "arguments",
        "audio",
        "completion",
        "content",
        "data",
        "delta",
        "function",
        "image_url",
        "input",
        "json",
        "logprobs",
        "message",
        "messages",
        "name",
        "output",
        "part",
        "reasoning",
        "reasoning_content",
        "refusal",
        "summary",
        "text",
        "thinking",
        "tool_calls",
        "tools",
    }
)
FREEFORM_CONTAINER_FIELDS = frozenset(
    {
        "annotations",
        "audio",
        "content",
        "data",
        "delta",
        "function",
        "message",
        "messages",
        "output",
        "part",
        "reasoning",
        "summary",
        "thinking",
        "tool_calls",
    }
)
_UNSET = object()
_OMITTED = object()


class UnsafeResponse(RuntimeError):
    pass


class InvalidJson(ValueError):
    pass


class RateLimitCapacity(RuntimeError):
    pass


def _rpm(value: int) -> int:
    if not isinstance(value, int) or isinstance(value, bool) or value < 0:
        raise ValueError("Tunnel RPM must be a non-negative integer")
    return value


def parse_publisher_profile(value: str) -> tuple[int, str]:
    match = PUBLISHER_PROFILE.fullmatch(value) if isinstance(value, str) else None
    if not match or not 20000 <= (port := int(match.group(1))) <= 29999:
        raise ValueError("Tunnel publisher profile is invalid")
    return port, match.group(2)


def publisher_url(value: str) -> str:
    _port, slug = parse_publisher_profile(value)
    return f"{PUBLISHER_URL_PREFIX}/{slug}/v1"


class _ClientRateLimits:
    def __init__(self, rpm: int) -> None:
        self._rpm = _rpm(rpm)
        self._lock = threading.Lock()
        self._gates: dict[str, list] = {}
        self._closed = False

    def configure(self, rpm: int) -> None:
        rpm = _rpm(rpm)
        with self._lock:
            if self._closed:
                return
            self._rpm = rpm
            gates = tuple(value[0] for value in self._gates.values())
            for gate in gates:
                gate.set_rpm(rpm)

    def _evict_locked(self, now: float) -> None:
        for client_ip, (gate, touched) in tuple(self._gates.items()):
            if (
                now - touched >= RATE_CLIENT_IDLE_SECONDS
                and gate.snapshot()["queued"] == 0
            ):
                del self._gates[client_ip]
                gate.close()

    def acquire(self, client_ip: str, cancelled: Callable[[], bool]) -> float:
        with self._lock:
            if self._closed:
                raise RelayStopping
            if not self._rpm:
                return 0.0
            now = time.monotonic()
            value = self._gates.get(client_ip)
            if value is None:
                self._evict_locked(now)
                if len(self._gates) >= MAX_RATE_CLIENTS:
                    raise RateLimitCapacity
                value = [RateGate(self._rpm), now]
                self._gates[client_ip] = value
            else:
                value[1] = now
            gate = value[0]
        try:
            return gate.acquire(lambda: self._closed or cancelled())
        finally:
            with self._lock:
                current = self._gates.get(client_ip)
                if current is not None and current[0] is gate:
                    current[1] = time.monotonic()

    def snapshot(self) -> dict[str, int]:
        with self._lock:
            return {
                "rpm_per_ip": self._rpm,
                "queued": sum(
                    value[0].snapshot()["queued"]
                    for value in self._gates.values()
                ),
            }

    def close(self) -> None:
        with self._lock:
            if self._closed:
                return
            self._closed = True
            for gate, _touched in self._gates.values():
                gate.close()


def _compact_text(value: str) -> str:
    folded = unicodedata.normalize("NFKC", value).casefold()
    return "".join(character for character in folded if character.isalnum())


def _marker_union(*groups: Iterable[str | bytes]) -> tuple[str | bytes, ...]:
    result = []
    seen = set()
    for group in groups:
        for marker in group:
            if not isinstance(marker, (str, bytes)):
                raise ValueError("Tunnel privacy marker is invalid")
            if not marker.strip() or marker in seen:
                continue
            seen.add(marker)
            result.append(marker)
    return tuple(result)


def _models(values: Iterable[str], *, require_nonempty: bool = False) -> frozenset[str]:
    if isinstance(values, (str, bytes)):
        raise ValueError("Tunnel model selection is invalid")
    result = set()
    for value in values:
        if (
            not isinstance(value, str)
            or not value
            or value != value.strip()
            or len(value) > 128
            or not value.isprintable()
        ):
            raise ValueError("Tunnel model selection is invalid")
        result.add(value)
        if len(result) > 256:
            raise ValueError("Too many tunnel models selected")
    if require_nonempty and not result:
        raise ValueError("Select at least one tunnel model")
    return frozenset(result)


def _token(value: str) -> str:
    if (
        not isinstance(value, str)
        or len(value) < 32
        or len(value) > 512
        or value != value.strip()
        or not value.isprintable()
    ):
        raise ValueError("Tunnel access key is invalid")
    return value


def _markers(values: Iterable[str | bytes], token: str) -> tuple[str, ...]:
    result = set()
    for value in (*values, token):
        if isinstance(value, bytes):
            try:
                value = value.decode("utf-8")
            except UnicodeDecodeError:
                for encoded in (
                    base64.b64encode(value).decode().rstrip("="),
                    base64.urlsafe_b64encode(value).decode().rstrip("="),
                ):
                    if encoded:
                        result.add(encoded.casefold())
                        if compact := _compact_text(encoded):
                            result.add(compact)
                continue
        if not isinstance(value, str):
            raise ValueError("Tunnel privacy marker is invalid")
        value = value.strip()
        if not value:
            continue
        forms = {
            value,
            *(unicodedata.normalize(form, value) for form in ("NFC", "NFD", "NFKC", "NFKD")),
        }
        variants = set(forms)
        for form in forms:
            encoded = form.encode()
            variants.update(
                (
                    base64.b64encode(encoded).decode().rstrip("="),
                    base64.urlsafe_b64encode(encoded).decode().rstrip("="),
                    quote(form, safe=""),
                    quote_plus(form, safe=""),
                    "".join(f"%{byte:02X}" for byte in encoded),
                )
            )
        for variant in variants:
            if variant:
                result.add(unicodedata.normalize("NFKC", variant).casefold())
                if compact := _compact_text(variant):
                    result.add(compact)
    return tuple(sorted(result, key=len, reverse=True))


def _contains_marker(
    value: str,
    markers: tuple[str, ...],
    *,
    reject_deep_encoding: bool = True,
) -> bool:
    candidates = {value}
    frontier = {value}
    for _depth in range(2):
        decoded = set()
        for candidate in frontier:
            if "%" not in candidate and "+" not in candidate:
                continue
            for decoder in (unquote, unquote_plus):
                try:
                    item = decoder(candidate, errors="strict")
                except UnicodeDecodeError:
                    continue
                if item not in candidates:
                    candidates.add(item)
                    decoded.add(item)
        if not decoded:
            break
        frontier = decoded
    for candidate in frontier:
        if "%" not in candidate and "+" not in candidate:
            continue
        for decoder in (unquote, unquote_plus):
            try:
                if (
                    reject_deep_encoding
                    and decoder(candidate, errors="strict") not in candidates
                ):
                    return True
            except UnicodeDecodeError:
                continue
    for candidate in candidates:
        folded = unicodedata.normalize("NFKC", candidate).casefold()
        if any(marker in folded for marker in markers):
            return True
        compact = _compact_text(folded)
        if compact and any(
            marker.isalnum() and marker in compact for marker in markers
        ):
            return True
    return False


def _safe_models(
    models: frozenset[str], markers: tuple[str, ...]
) -> frozenset[str]:
    safe = frozenset(
        model for model in models if not _contains_marker(model, markers)
    )
    return frozenset() if _contains_marker("".join(sorted(safe)), markers) else safe


def _private_field(name: str) -> bool:
    compact = _compact_text(name)
    return (
        "provider" in compact
        or "upstream" in compact
        or "proxy" in compact
        or "credential" in compact
        or "secret" in compact
        or compact.endswith("apikey")
        or compact.endswith("authorization")
        or compact
        in {
            "accesskey",
            "accesstoken",
            "apiurl",
            "auth",
            "authentication",
            "authorization",
            "authtoken",
            "backend",
            "baseurl",
            "bearer",
            "bearertoken",
            "clientkey",
            "cookie",
            "deployment",
            "endpoint",
            "gateway",
            "headers",
            "host",
            "key",
            "organization",
            "organizationid",
            "ownedby",
            "password",
            "passwd",
            "privatekey",
            "requestheaders",
            "requestid",
            "responseheaders",
            "server",
            "servicetier",
            "sessiontoken",
            "setcookie",
            "systemfingerprint",
            "token",
            "vendor",
            "xapikey",
            "url",
        }
    )


def _unique_object(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise InvalidJson("Duplicate JSON key")
        value[key] = item
    return value


def _load_json(body: bytes | str):
    def reject_constant(_value):
        raise InvalidJson("Non-finite JSON number")

    try:
        return json.loads(
            body,
            object_pairs_hook=_unique_object,
            parse_constant=reject_constant,
        )
    except (ValueError, UnicodeDecodeError, RecursionError) as error:
        raise InvalidJson("Invalid JSON") from error


def _semantic_strings(
    value,
    fragments,
    depth=0,
    field="",
) -> None:
    if depth > 128:
        raise UnsafeResponse
    all_strings, keys, values, fields = fragments
    if isinstance(value, dict):
        for key, item in value.items():
            all_strings.append(key)
            keys.append(key)
            _semantic_strings(item, fragments, depth + 1, key.casefold())
    elif isinstance(value, list):
        for item in value:
            _semantic_strings(item, fragments, depth + 1, field)
    elif isinstance(value, str):
        all_strings.append(value)
        values.append(value)
        if field:
            fields.setdefault(field, []).append(value)


def _semantic_candidates(value, fragments=None, extra_strings=()) -> tuple[str, ...]:
    collected = fragments or ([], [], [], {})
    if value is not None:
        _semantic_strings(value, collected)
    candidates = ["".join(group) for group in collected[:3]]
    candidates.extend("".join(group) for group in collected[3].values())
    if extra_strings:
        extra = "".join(extra_strings)
        semantic = tuple(candidates)
        candidates.extend((extra, *(extra + item for item in semantic)))
        candidates.extend(item + extra for item in semantic)
    return tuple(candidates)


def _semantic_safe(
    value,
    markers: tuple[str, ...],
    fragments=None,
    extra_strings=(),
    *,
    reject_deep_encoding=True,
) -> None:
    candidates = _semantic_candidates(value, fragments, extra_strings)
    if any(
        _contains_marker(
            candidate,
            markers,
            reject_deep_encoding=reject_deep_encoding,
        )
        for candidate in candidates
    ):
        raise UnsafeResponse


def _metadata_projection(value, depth=0, suppress_scalar=False):
    """Return only provider-controlled structure, excluding model free-form data.

    Hidden identity markers are never compared against model-controlled text. That
    comparison is an unavoidable membership oracle because a model can transform
    attacker input arbitrarily before echoing it. Credentials are checked against
    the full response separately.
    """

    if depth > 128:
        raise UnsafeResponse
    if isinstance(value, dict):
        projected = {}
        for key, item in value.items():
            field = key.casefold()
            if (
                field in FREEFORM_RESPONSE_FIELDS
                and field not in FREEFORM_CONTAINER_FIELDS
            ):
                continue
            child = _metadata_projection(
                item,
                depth + 1,
                suppress_scalar=field in FREEFORM_CONTAINER_FIELDS,
            )
            if child is not _OMITTED:
                projected[key] = child
        return projected
    if isinstance(value, list):
        projected = []
        for item in value:
            child = _metadata_projection(
                item,
                depth + 1,
                suppress_scalar=suppress_scalar,
            )
            if child is not _OMITTED:
                projected.append(child)
        return projected
    if suppress_scalar:
        return _OMITTED
    return value


def _sanitize_json(value, requested_model: str, markers: tuple[str, ...], depth=0):
    if depth > 128:
        raise UnsafeResponse
    if isinstance(value, dict):
        clean = {}
        for key, item in value.items():
            if not isinstance(key, str):
                raise UnsafeResponse
            if _private_field(key):
                continue
            clean[key] = (
                requested_model
                if key.casefold() == "model"
                else _sanitize_json(item, requested_model, markers, depth + 1)
            )
        return clean
    if isinstance(value, list):
        return [
            _sanitize_json(item, requested_model, markers, depth + 1)
            for item in value
        ]
    return value


def _json_bytes(value) -> bytes:
    try:
        return json.dumps(
            value,
            ensure_ascii=False,
            separators=(",", ":"),
            allow_nan=False,
        ).encode("utf-8")
    except UnicodeEncodeError as error:
        raise UnsafeResponse from error


def _sanitize_sse_event(
    event: bytes,
    requested_model: str,
    markers: tuple[str, ...],
    metadata_fragments,
    content_fragments,
) -> bytes:
    try:
        text = event.decode("utf-8")
    except UnicodeDecodeError as error:
        raise UnsafeResponse from error
    data = []
    fields = []
    for line in text.splitlines():
        if line.startswith("data:"):
            data.append(line[5:].lstrip())
        elif line.startswith(("event:", "id:", "retry:")):
            fields.append(line)
            name, _separator, field_value = line.partition(":")
            for fragments in (metadata_fragments, content_fragments):
                fragments[0].extend((name, field_value.lstrip()))
                fragments[1].append(name)
                fragments[2].append(field_value.lstrip())
                fragments[3].setdefault(name.casefold(), []).append(
                    field_value.lstrip()
                )
    if not data:
        return (("\n".join(fields) + "\n\n").encode("utf-8") if fields else b"")
    payload = "\n".join(data)
    if payload == "[DONE]":
        clean_data = "[DONE]"
    else:
        try:
            value = _load_json(payload)
        except InvalidJson as error:
            raise UnsafeResponse from error
        clean_value = _sanitize_json(value, requested_model, markers)
        _semantic_strings(
            _metadata_projection(clean_value), metadata_fragments
        )
        _semantic_strings(clean_value, content_fragments)
        clean_data = _json_bytes(
            clean_value
        ).decode("utf-8")
    return ("\n".join((*fields, f"data: {clean_data}")) + "\n\n").encode("utf-8")


class TunnelGateway(ThreadingHTTPServer):
    daemon_threads = True
    request_queue_size = 128

    def __init__(
        self,
        relay_address: tuple[str, int],
        token: str,
        allowed_models: Iterable[str],
        *,
        sensitive_markers: Iterable[str | bytes] = (),
        secret_markers: Iterable[str | bytes] = (),
        route_guard: Callable[[], bool] | None = None,
        policy_lock=None,
        start_inactive: bool = False,
        readiness_token: str = "",
        public_rpm: int = 0,
    ) -> None:
        host, port = relay_address
        try:
            loopback = ipaddress.ip_address(host).is_loopback
        except ValueError:
            loopback = host.casefold() == "localhost"
        if not loopback or not 1 <= int(port) <= 65535:
            raise ValueError("Tunnel relay address must be loopback")
        self.relay_address = (host, int(port))
        self._policy_lock = policy_lock or threading.RLock()
        self._lock = threading.Lock()
        self._route_guard = route_guard or (lambda: True)
        self._active = not start_inactive
        self._readiness_token = _token(readiness_token) if readiness_token else ""
        self._public_rpm = _rpm(public_rpm)
        self._rate_limits = _ClientRateLimits(self._public_rpm)
        self._token = _token(token)
        self._token_history = (self._token,)
        self._request_markers = _markers(self._token_history, self._token)
        selected_models = _models(allowed_models, require_nonempty=True)
        self._selected_models = selected_models
        self._raw_markers = tuple(sensitive_markers)
        self._raw_secret_markers = tuple(secret_markers)
        self._marker_history = _marker_union(self._raw_markers, (self._token,))
        self._content_marker_history = _marker_union(
            self._raw_secret_markers, (self._token,)
        )
        self._markers = _markers(self._marker_history, self._token)
        self._content_markers = _markers(
            self._content_marker_history, self._token
        )
        self._allowed_models = _safe_models(selected_models, self._markers)
        if not self._allowed_models:
            raise ValueError("No selected model is safe to expose")
        super().__init__(("127.0.0.1", 0), TunnelHandler)

    @property
    def port(self) -> int:
        return self.server_port

    def configure(
        self,
        *,
        token=_UNSET,
        allowed_models=_UNSET,
        sensitive_markers=_UNSET,
        secret_markers=_UNSET,
        public_rpm=_UNSET,
    ) -> None:
        with self._policy_lock:
            with self._lock:
                self._configure_locked(
                    token,
                    allowed_models,
                    sensitive_markers,
                    secret_markers,
                    public_rpm,
                )

    def _configure_locked(
        self,
        token,
        allowed_models,
        sensitive_markers,
        secret_markers,
        public_rpm,
    ) -> None:
        next_token = self._token if token is _UNSET else _token(token)
        next_selected_models = (
            self._selected_models
            if allowed_models is _UNSET
            else _models(allowed_models)
        )
        next_raw_markers = (
            self._raw_markers
            if sensitive_markers is _UNSET
            else tuple(sensitive_markers)
        )
        next_raw_secret_markers = (
            self._raw_secret_markers
            if secret_markers is _UNSET
            else tuple(secret_markers)
        )
        next_public_rpm = (
            self._public_rpm if public_rpm is _UNSET else _rpm(public_rpm)
        )
        next_history = _marker_union(
            self._marker_history,
            next_raw_markers,
            (next_token,),
        )
        next_token_history = _marker_union(self._token_history, (next_token,))
        next_content_history = _marker_union(
            self._content_marker_history,
            next_raw_secret_markers,
            (next_token,),
        )
        next_markers = _markers(next_history, next_token)
        next_content_markers = _markers(next_content_history, next_token)
        next_request_markers = _markers(next_token_history, next_token)
        next_models = _safe_models(next_selected_models, next_markers)
        self._token = next_token
        self._token_history = next_token_history
        self._request_markers = next_request_markers
        self._selected_models = next_selected_models
        self._allowed_models = next_models
        self._raw_markers = next_raw_markers
        self._raw_secret_markers = next_raw_secret_markers
        self._marker_history = next_history
        self._content_marker_history = next_content_history
        self._markers = next_markers
        self._content_markers = next_content_markers
        self._public_rpm = next_public_rpm
        self._rate_limits.configure(next_public_rpm)

    def activate(
        self,
        *,
        token=_UNSET,
        allowed_models=_UNSET,
        sensitive_markers=_UNSET,
        secret_markers=_UNSET,
        public_rpm=_UNSET,
    ) -> None:
        with self._policy_lock:
            with self._lock:
                self._configure_locked(
                    token,
                    allowed_models,
                    sensitive_markers,
                    secret_markers,
                    public_rpm,
                )
                if not self._allowed_models:
                    raise ValueError("Select at least one safe tunnel model")
                self._active = True
                self._readiness_token = ""

    def acquire(self, client_ip: str, cancelled: Callable[[], bool]) -> float:
        return self._rate_limits.acquire(client_ip, cancelled)

    def rate_snapshot(self) -> dict[str, int]:
        return self._rate_limits.snapshot()

    def shutdown(self) -> None:
        self._rate_limits.close()
        super().shutdown()

    def server_close(self) -> None:
        self._rate_limits.close()
        super().server_close()

    def policy(
        self,
    ) -> tuple:
        with self.final_policy() as policy:
            return policy

    @contextmanager
    def final_policy(self, readiness_token: str = ""):
        with self._policy_lock:
            with self._lock:
                readiness_allowed = bool(
                    not self._active
                    and self._readiness_token
                    and readiness_token
                    and hmac.compare_digest(
                        readiness_token.encode(), self._readiness_token.encode()
                    )
                )
                policy = (
                    self._token,
                    self._allowed_models,
                    self._markers,
                    self._content_markers,
                    self._request_markers,
                    self._active or readiness_allowed,
                )
            try:
                route_allowed = policy[5] and bool(self._route_guard())
            except Exception:
                route_allowed = False
            yield policy[0], policy[1], policy[2], policy[3], policy[4], route_allowed


class TunnelHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = ""
    sys_version = ""

    @property
    def gateway(self) -> TunnelGateway:
        return self.server

    def log_message(self, _format, *_args) -> None:
        pass

    def setup(self) -> None:
        super().setup()
        self.connection.settimeout(CLIENT_READ_TIMEOUT)

    def send_error(self, code, _message=None, _explain=None) -> None:
        self._error(405 if code == 501 else 400, "Method not allowed" if code == 501 else "Invalid request")

    def handle_expect_100(self) -> bool:
        self._error(400, "Invalid request")
        return False

    def _headers(self, status: int, content_type: str, length: int | None) -> None:
        self.send_response_only(status)
        self.send_header("Content-Type", content_type)
        if length is not None:
            self.send_header("Content-Length", str(length))
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True

    def _commit(self, status: int, content_type: str, length: int) -> bool:
        try:
            self.connection.settimeout(CLIENT_WRITE_TIMEOUT)
            self._headers(status, content_type, length)
            return True
        except OSError:
            self.close_connection = True
            return False

    def _write_committed(self, body: bytes) -> None:
        if self.command == "HEAD":
            return
        try:
            self.connection.settimeout(CLIENT_WRITE_TIMEOUT)
            self.wfile.write(body)
            self.wfile.flush()
        except OSError:
            self.close_connection = True

    def _respond(self, status: int, content_type: str, body: bytes) -> None:
        if self._commit(status, content_type, len(body)):
            self._write_committed(body)

    def _error(self, status: int, message: str) -> None:
        self._respond(status, "application/json", _json_bytes({"error": message}))

    def _authorized(self, expected: str) -> bool:
        authorization_values = self.headers.get_all("Authorization", [])
        api_key_values = self.headers.get_all("x-api-key", [])
        if len(authorization_values) > 1 or len(api_key_values) > 1:
            return False
        authorization = authorization_values[0] if authorization_values else ""
        scheme, separator, bearer = authorization.partition(" ")
        candidates = []
        if separator and scheme.casefold() == "bearer":
            candidates.append(bearer.strip())
        api_key = api_key_values[0].strip() if api_key_values else ""
        if api_key:
            candidates.append(api_key)
        return any(
            hmac.compare_digest(candidate.encode(), expected.encode())
            for candidate in candidates
        )

    def _canonical_path(self) -> str | None:
        if not self.path.startswith("/") or "?" in self.path or "#" in self.path:
            return None
        return self.path if self.path.isascii() else None

    def _client_ip(self) -> str | None:
        values = self.headers.get_all(CLIENT_IP_HEADER, [])
        if len(values) != 1:
            return None
        value = values[0]
        if value != value.strip() or not value.isascii() or "%" in value:
            return None
        try:
            address = ipaddress.ip_address(value)
        except ValueError:
            return None
        if isinstance(address, ipaddress.IPv6Address) and address.ipv4_mapped:
            address = address.ipv4_mapped
        return str(address)

    def _client_disconnected(self) -> bool:
        try:
            readable, _writable, _exceptional = select.select(
                (self.connection,), (), (), 0
            )
            return bool(readable) and not self.connection.recv(1, socket.MSG_PEEK)
        except (OSError, ValueError):
            return True

    def do_GET(self) -> None:
        readiness_values = self.headers.get_all(READINESS_HEADER, [])
        readiness_token = readiness_values[0] if len(readiness_values) == 1 else ""
        error = body = None
        with self.gateway.final_policy(readiness_token) as (
            token,
            models,
            _markers_value,
            _content_markers,
            _request_markers,
            route_allowed,
        ):
            if not self._authorized(token):
                error = (401, "Unauthorized")
            elif self._canonical_path() != "/v1/models":
                error = (404, "Not found")
            elif not route_allowed:
                error = (403, "Request rejected")
            else:
                body = _json_bytes(
                    {
                        "object": "list",
                        "data": [
                            {"id": model, "object": "model"}
                            for model in sorted(models)
                        ],
                    }
                )
        if error:
            self._error(*error)
        elif self._commit(200, "application/json", len(body)):
            self._write_committed(body)

    def _body_length(self) -> int:
        if self.headers.get_all("Transfer-Encoding") or self.headers.get_all("Content-Encoding"):
            raise ValueError
        content_types = self.headers.get_all("Content-Type", [])
        if len(content_types) != 1:
            raise ValueError
        content_type = content_types[0].partition(";")[0].strip()
        if content_type.casefold() != "application/json":
            raise ValueError
        lengths = self.headers.get_all("Content-Length", [])
        if len(lengths) != 1:
            raise ValueError
        raw_length = lengths[0].strip()
        if not raw_length.isascii() or not raw_length.isdecimal():
            raise ValueError
        length = int(raw_length)
        if length > MAX_BODY_BYTES:
            raise OverflowError
        if length <= 0:
            raise ValueError
        return length

    def _body(self, length: int) -> tuple[bytes, dict]:
        try:
            body = self.rfile.read(length)
        except OSError as error:
            raise ValueError from error
        if len(body) != length:
            raise ValueError
        try:
            value = _load_json(body)
        except InvalidJson as error:
            raise ValueError from error
        if not isinstance(value, dict):
            raise ValueError
        return body, value

    def _policy_rejection(
        self,
        token: str,
        models: frozenset[str],
        _response_markers: tuple[str, ...],
        _content_markers: tuple[str, ...],
        request_markers: tuple[str, ...],
        route_allowed: bool,
        requested_model: str,
        payload: dict,
        forwarded_values=(),
    ) -> tuple[int, str] | None:
        if not self._authorized(token):
            return 401, "Unauthorized"
        if not route_allowed or requested_model not in models:
            return 403, "Request rejected"
        try:
            _semantic_safe(
                payload,
                request_markers,
                extra_strings=forwarded_values,
                reject_deep_encoding=False,
            )
        except UnsafeResponse:
            return 403, "Request rejected"
        return None

    def do_POST(self) -> None:
        (
            token,
            _models_value,
            _markers_value,
            _content_markers,
            _request_markers,
            route_allowed,
        ) = self.gateway.policy()
        if not self._authorized(token):
            self._error(401, "Unauthorized")
            return
        path = self._canonical_path()
        if path not in PUBLIC_ROUTES:
            self._error(404, "Not found")
            return
        if not route_allowed:
            self._error(403, "Request rejected")
            return
        client_ip = self._client_ip()
        if client_ip is None:
            self._error(400, "Invalid request")
            return
        try:
            length = self._body_length()
        except OverflowError:
            self._error(413, "Request too large")
            return
        except ValueError:
            self._error(400, "Invalid request")
            return
        try:
            self.gateway.acquire(client_ip, self._client_disconnected)
        except (ClientDisconnected, RelayStopping, RateLimitCapacity):
            self._error(503, "Request unavailable")
            return
        try:
            body, payload = self._body(length)
        except ValueError:
            self._error(400, "Invalid request")
            return
        requested_model = payload.get("model")
        if not isinstance(requested_model, str) or not requested_model:
            self._error(400, "Invalid request")
            return
        with self.gateway.final_policy() as current:
            rejection = self._policy_rejection(
                *current, requested_model, payload
            )
        if rejection:
            self._error(*rejection)
            return
        self._forward(path, body, payload, requested_model)

    def _unsupported(self) -> None:
        (
            token,
            _models_value,
            _markers_value,
            _content_markers,
            _request_markers,
            _route_allowed,
        ) = self.gateway.policy()
        if not self._authorized(token):
            self._error(401, "Unauthorized")
            return
        path = self._canonical_path()
        known = path == "/v1/models" or path in PUBLIC_ROUTES
        self._error(
            405 if known else 404,
            "Method not allowed" if known else "Not found",
        )

    do_DELETE = do_HEAD = do_OPTIONS = do_PATCH = do_PUT = do_TRACE = _unsupported

    def _forward(
        self, path: str, body: bytes, payload: dict, requested_model: str
    ) -> None:
        headers = {
            "Content-Type": "application/json",
            "Accept-Encoding": "identity",
            "Connection": "close",
            "X-Provider-Switch-Tunnel": "1",
        }
        forwarded_values = []
        for incoming, outgoing in SAFE_REQUEST_HEADERS.items():
            values = self.headers.get_all(incoming, [])
            if len(values) > 1 or any(
                not value or len(value) > 1024 or not value.isprintable()
                for value in values
            ):
                self._error(400, "Invalid request")
                return
            if values:
                headers[outgoing] = values[0]
                forwarded_values.append(values[0])
        connection = http.client.HTTPConnection(*self.gateway.relay_address, timeout=300)
        response = None
        try:
            with self.gateway.final_policy() as current:
                rejection = self._policy_rejection(
                    *current, requested_model, payload, forwarded_values
                )
            if rejection:
                self._error(*rejection)
                return
            connection.request("POST", path, body=body, headers=headers)
            response = connection.getresponse()
            if not 200 <= response.status < 300:
                raise UnsafeResponse
            if response.getheader("Content-Encoding") is not None:
                raise UnsafeResponse
            content_type = (response.getheader("Content-Type") or "").partition(";")[0].strip().casefold()
            if content_type not in {"text/event-stream", "application/json"} and not content_type.endswith("+json"):
                raise UnsafeResponse
            raw_response = self._response_body(response)
            # Public AI routes have one success shape. Do not expose an
            # upstream-specific 2xx choice as a provider fingerprint.
            status = 200
        except (OSError, http.client.HTTPException, UnsafeResponse):
            self._error(502, "Upstream response rejected")
            return
        finally:
            if response is not None:
                response.close()
            connection.close()

        rejection = sanitize_error = None
        with self.gateway.final_policy() as current:
            rejection = self._policy_rejection(
                *current, requested_model, payload, forwarded_values
            )
            if not rejection:
                markers = current[2]
                content_markers = current[3]
                try:
                    if content_type == "text/event-stream":
                        clean = self._buffer_sse(
                            raw_response,
                            requested_model,
                            markers,
                            content_markers,
                        )
                        public_type = "text/event-stream"
                    else:
                        clean = self._buffer_json(
                            raw_response,
                            requested_model,
                            markers,
                            content_markers,
                        )
                        public_type = "application/json"
                except UnsafeResponse:
                    sanitize_error = (502, "Upstream response rejected")
        if rejection:
            self._error(*rejection)
        elif sanitize_error:
            self._error(*sanitize_error)
        elif self._commit(status, public_type, len(clean)):
            self._write_committed(clean)

    @staticmethod
    def _response_body(response: http.client.HTTPResponse) -> bytes:
        declared = response.getheader("Content-Length")
        if declared:
            if not declared.isascii() or not declared.isdecimal():
                raise UnsafeResponse
            if int(declared) > MAX_BODY_BYTES:
                raise UnsafeResponse
        body = response.read(MAX_BODY_BYTES + 1)
        if len(body) > MAX_BODY_BYTES:
            raise UnsafeResponse
        if declared and len(body) != int(declared):
            raise UnsafeResponse
        return body

    def _buffer_json(
        self,
        body: bytes,
        requested_model: str,
        markers: tuple[str, ...],
        content_markers: tuple[str, ...],
    ) -> bytes:
        try:
            value = _load_json(body)
        except InvalidJson as error:
            raise UnsafeResponse from error
        clean = _sanitize_json(value, requested_model, markers)
        _semantic_safe(_metadata_projection(clean), markers)
        _semantic_safe(clean, content_markers)
        return _json_bytes(clean)

    def _buffer_sse(
        self,
        body: bytes,
        requested_model: str,
        markers: tuple[str, ...],
        content_markers: tuple[str, ...],
    ) -> bytes:
        clean = bytearray()
        metadata_fragments = ([], [], [], {})
        content_fragments = ([], [], [], {})
        start = 0
        for match in re.finditer(br"\r?\n\r?\n", body):
            event = body[start : match.start()]
            if len(event) > MAX_EVENT_BYTES:
                raise UnsafeResponse
            clean.extend(
                _sanitize_sse_event(
                    event,
                    requested_model,
                    markers,
                    metadata_fragments,
                    content_fragments,
                )
            )
            start = match.end()
        tail = body[start:]
        if tail.strip():
            if len(tail) > MAX_EVENT_BYTES:
                raise UnsafeResponse
            clean.extend(
                _sanitize_sse_event(
                    tail,
                    requested_model,
                    markers,
                    metadata_fragments,
                    content_fragments,
                )
            )
        if not clean or len(clean) > MAX_BODY_BYTES:
            raise UnsafeResponse
        _semantic_safe(None, markers, metadata_fragments)
        _semantic_safe(None, content_markers, content_fragments)
        return bytes(clean)


def _find_ssh() -> str | None:
    windows = os.environ.get("WINDIR") or os.environ.get("SYSTEMROOT")
    executable = Path(windows) / "System32/OpenSSH/ssh.exe" if windows else None
    return str(executable) if executable is not None and executable.is_file() else None


def _find_identity(name: str) -> str | None:
    local_app_data = os.environ.get("LOCALAPPDATA")
    identity = (
        Path(local_app_data) / "ProviderSwitchboard/ssh" / name
        if local_app_data
        else None
    )
    return str(identity) if identity is not None and identity.is_file() else None


def _find_ssh_identity() -> str | None:
    return _find_identity("model-tunnel_ed25519")


def _find_control_identity() -> str | None:
    return _find_identity(CONTROL_SSH_IDENTITY_NAME)


def _ensure_ssh_known_hosts(identity: str) -> str:
    destination = Path(identity).with_name(SSH_KNOWN_HOSTS_NAME)
    expected = f"{SSH_HOST} {SSH_HOST_KEY}\n"
    try:
        if destination.read_text(encoding="ascii") == expected:
            os.chmod(destination, 0o600)
            return str(destination)
    except (FileNotFoundError, OSError, UnicodeError):
        pass

    temporary = destination.with_name(
        f".{destination.name}.{os.getpid()}.{secrets.token_hex(8)}.tmp"
    )
    descriptor = None
    try:
        descriptor = os.open(
            temporary,
            os.O_WRONLY | os.O_CREAT | os.O_EXCL,
            0o600,
        )
        with os.fdopen(descriptor, "w", encoding="ascii", newline="\n") as stream:
            descriptor = None
            stream.write(expected)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, destination)
        os.chmod(destination, 0o600)
    finally:
        if descriptor is not None:
            os.close(descriptor)
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass
    return str(destination)


def _https_models_ready(
    base_url: str,
    token: str,
    readiness_token: str,
    expected_models: frozenset[str],
    timeout: float,
) -> bool:
    connection = response = None
    try:
        endpoint = urlsplit(base_url)
        if (
            endpoint.scheme != "https"
            or not endpoint.hostname
            or endpoint.username is not None
            or endpoint.password is not None
            or endpoint.query
            or endpoint.fragment
        ):
            return False
        target = endpoint.path.rstrip("/") + "/models"
        connection = http.client.HTTPSConnection(
            endpoint.hostname,
            endpoint.port or 443,
            timeout=max(0.1, timeout),
        )
        connection.request(
            "GET",
            target,
            headers={
                "Authorization": f"Bearer {token}",
                READINESS_HEADER: readiness_token,
                "Accept": "application/json",
                "Accept-Encoding": "identity",
                "Connection": "close",
            },
        )
        response = connection.getresponse()
        lengths = response.headers.get_all("Content-Length", [])
        if (
            response.status != 200
            or response.getheader("Content-Encoding") is not None
            or (response.getheader("Content-Type") or "")
            .partition(";")[0]
            .strip()
            .casefold()
            != "application/json"
            or len(lengths) != 1
            or not lengths[0].isascii()
            or not lengths[0].isdecimal()
            or int(lengths[0]) > MAX_MODELS_BYTES
        ):
            return False
        body = response.read(MAX_MODELS_BYTES + 1)
        if len(body) != int(lengths[0]):
            return False
        value = _load_json(body)
        if not isinstance(value, dict):
            return False
        data = value.get("data")
        if value.get("object") != "list" or not isinstance(data, list):
            return False
        models = []
        for item in data:
            if (
                not isinstance(item, dict)
                or item.get("object") != "model"
                or not isinstance(item.get("id"), str)
            ):
                return False
            models.append(item["id"])
        return len(models) == len(set(models)) and frozenset(models) == expected_models
    except (OSError, ValueError, UnicodeError, http.client.HTTPException, InvalidJson):
        return False
    finally:
        if response is not None:
            response.close()
        if connection is not None:
            connection.close()


def _child_environment() -> dict[str, str]:
    allowed = (
        "SYSTEMROOT",
        "WINDIR",
        "TEMP",
        "TMP",
        "USERPROFILE",
        "LOCALAPPDATA",
        "PROGRAMDATA",
        "HOMEDRIVE",
        "HOMEPATH",
        "USERNAME",
        "USERDOMAIN",
    )
    return {name: os.environ[name] for name in allowed if name in os.environ}


def _ssh_base_command(
    executable: str,
    identity: str,
    known_hosts: str,
) -> list[str]:
    return [
        executable,
        "-T",
        "-F",
        "NUL",
        "-i",
        identity,
        "-o", "BatchMode=yes",
        "-o", "ExitOnForwardFailure=yes",
        "-o", "ServerAliveInterval=15",
        "-o", "ServerAliveCountMax=3",
        "-o", "LogLevel=ERROR",
        "-o", "ForwardAgent=no",
        "-o", "ForwardX11Trusted=no",
        "-o", "IdentitiesOnly=yes",
        "-o", "StrictHostKeyChecking=yes",
        "-o", f"UserKnownHostsFile={known_hosts}",
        "-o", "GlobalKnownHostsFile=NUL",
        "-o", "HostKeyAlgorithms=ssh-ed25519",
        "-o", "UpdateHostKeys=no",
        "-o", "IdentityAgent=none",
        "-o", "PubkeyAuthentication=yes",
        "-o", "PreferredAuthentications=publickey",
        "-o", "PasswordAuthentication=no",
        "-o", "KbdInteractiveAuthentication=no",
        "-o", "ChallengeResponseAuthentication=no",
        "-o", "ForwardX11=no",
        "-o", "PermitLocalCommand=no",
        "-o", "RequestTTY=no",
    ]


def _parse_control_snapshot(output: bytes) -> dict:
    if (
        not isinstance(output, bytes)
        or not output
        or len(output) > CONTROL_OUTPUT_BYTES
        or not output.endswith(b"\n")
        or b"\n" in output[:-1]
        or b"\r" in output
    ):
        raise ValueError(CONTROL_ERROR)
    value = _load_json(output[:-1])
    if (
        not isinstance(value, dict)
        or set(value) != {"v", "ok", "revision", "tunnels"}
        or type(value["v"]) is not int
        or value["v"] != 1
        or value["ok"] is not True
        or type(value["revision"]) is not int
        or not 0 <= value["revision"] <= 2**63 - 1
        or not isinstance(value["tunnels"], list)
        or len(value["tunnels"]) > 256
    ):
        raise ValueError(CONTROL_ERROR)
    tunnels = []
    names = set()
    self_count = 0
    for position, item in enumerate(value["tunnels"]):
        if (
            not isinstance(item, dict)
            or set(item) != {"name", "state"}
            or not isinstance(item["name"], str)
            or (
                item["name"] != CONTROL_SELF_NAME
                and CONTROL_TUNNEL_NAME.fullmatch(item["name"]) is None
            )
            or not isinstance(item["state"], str)
            or item["state"] not in {"running", "paused", "stopped"}
            or item["name"] in names
        ):
            raise ValueError(CONTROL_ERROR)
        self_count += int(item["name"] == CONTROL_SELF_NAME)
        if self_count > 1:
            raise ValueError(CONTROL_ERROR)
        names.add(item["name"])
        tunnels.append(
            {"position": position, "name": item["name"], "state": item["state"]}
        )
    return {
        "available": True,
        "revision": value["revision"],
        "tunnels": tunnels,
        "error": "",
    }


class SharedTunnelControl:
    def __init__(self) -> None:
        self._lock = threading.Lock()

    @staticmethod
    def _unavailable() -> dict:
        return {
            "available": False,
            "revision": 0,
            "tunnels": [],
            "error": CONTROL_ERROR,
        }

    def _request(self, *remote_command: str) -> dict:
        executable = _find_ssh()
        identity = _find_control_identity()
        if not executable or not identity:
            raise RuntimeError(CONTROL_ERROR)
        try:
            known_hosts = _ensure_ssh_known_hosts(identity)
            completed = subprocess.run(
                [
                    *_ssh_base_command(executable, identity, known_hosts),
                    "-o", "ClearAllForwardings=yes",
                    "-o", "ConnectTimeout=5",
                    CONTROL_SSH_DESTINATION,
                    "v1",
                    *remote_command,
                ],
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                env=_child_environment(),
                timeout=CONTROL_TIMEOUT,
                check=False,
                creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
                shell=False,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            raise RuntimeError(CONTROL_ERROR) from error
        if completed.returncode != 0:
            raise RuntimeError(CONTROL_ERROR)
        try:
            return _parse_control_snapshot(completed.stdout)
        except (InvalidJson, ValueError) as error:
            raise RuntimeError(CONTROL_ERROR) from error

    def snapshot(self) -> dict:
        with self._lock:
            try:
                return self._request("list")
            except RuntimeError:
                return self._unavailable()

    def control(self, position: int, revision: int, action: str) -> dict:
        if (
            type(position) is not int
            or not 0 <= position < 256
            or type(revision) is not int
            or not 0 <= revision <= 2**63 - 1
            or not isinstance(action, str)
            or action not in CONTROL_ACTIONS
        ):
            raise ValueError("Shared tunnel control request is invalid")
        with self._lock:
            return self._request(action, str(revision), str(position))


class TunnelController:
    def __init__(
        self,
        relay_address: tuple[str, int],
        token: str,
        allowed_models: Iterable[str],
        *,
        sensitive_markers: Iterable[str | bytes] = (),
        secret_markers: Iterable[str | bytes] = (),
        route_guard: Callable[[], bool] | None = None,
        startup_timeout: float = 20.0,
        policy_lock=None,
        public_rpm: int = 0,
        publisher_profile: str = DEFAULT_PUBLISHER_PROFILE,
        readiness_delay: float = READINESS_INITIAL_DELAY,
    ) -> None:
        self._policy_lock = policy_lock or threading.RLock()
        self.relay_address = relay_address
        self._token = _token(token)
        self._sensitive_markers = tuple(sensitive_markers)
        self._secret_markers = tuple(secret_markers)
        self._marker_history = _marker_union(
            self._sensitive_markers,
            (self._token,),
        )
        self._secret_marker_history = _marker_union(
            self._secret_markers,
            (self._token,),
        )
        self._selected_models = _models(allowed_models)
        self._allowed_models = _safe_models(
            self._selected_models,
            _markers(self._marker_history, self._token),
        )
        self._route_guard = route_guard
        self._startup_timeout = max(1.0, float(startup_timeout))
        self._readiness_delay = max(0.0, float(readiness_delay))
        self._public_rpm = _rpm(public_rpm)
        parse_publisher_profile(publisher_profile)
        self._publisher_profile = publisher_profile
        self._lock = threading.Lock()
        self._operation_lock = threading.Lock()
        self._state = "stopped"
        self._url = self._error = ""
        self._gateway = self._gateway_thread = self._process = None

    def configure(
        self,
        *,
        token=_UNSET,
        allowed_models=_UNSET,
        sensitive_markers=_UNSET,
        secret_markers=_UNSET,
        public_rpm=_UNSET,
        publisher_profile=_UNSET,
    ) -> None:
        with self._policy_lock:
            with self._lock:
                next_token = self._token if token is _UNSET else _token(token)
                next_selected_models = (
                    self._selected_models
                    if allowed_models is _UNSET
                    else _models(allowed_models)
                )
                next_markers = (
                    self._sensitive_markers
                    if sensitive_markers is _UNSET
                    else tuple(sensitive_markers)
                )
                next_secret_markers = (
                    self._secret_markers
                    if secret_markers is _UNSET
                    else tuple(secret_markers)
                )
                next_public_rpm = (
                    self._public_rpm
                    if public_rpm is _UNSET
                    else _rpm(public_rpm)
                )
                next_publisher_profile = (
                    self._publisher_profile
                    if publisher_profile is _UNSET
                    else publisher_profile
                )
                parse_publisher_profile(next_publisher_profile)
                if (
                    next_publisher_profile != self._publisher_profile
                    and self._state not in {"stopped", "error"}
                ):
                    raise RuntimeError(
                        "Stop the tunnel before changing its publisher profile"
                    )
                next_history = _marker_union(
                    self._marker_history,
                    next_markers,
                    (next_token,),
                )
                next_secret_history = _marker_union(
                    self._secret_marker_history,
                    next_secret_markers,
                    (next_token,),
                )
                next_models = _safe_models(
                    next_selected_models, _markers(next_history, next_token)
                )
                gateway = self._gateway
                if gateway is not None:
                    gateway.configure(
                        token=next_token,
                        allowed_models=next_selected_models,
                        sensitive_markers=next_history,
                        secret_markers=next_secret_history,
                        public_rpm=next_public_rpm,
                    )
                self._token = next_token
                self._selected_models = next_selected_models
                self._allowed_models = next_models
                self._sensitive_markers = next_markers
                self._secret_markers = next_secret_markers
                self._marker_history = next_history
                self._secret_marker_history = next_secret_history
                self._public_rpm = next_public_rpm
                self._publisher_profile = next_publisher_profile

    def _snapshot_locked(self) -> dict:
        rates = (
            self._gateway.rate_snapshot()
            if self._gateway is not None
            else {"rpm_per_ip": self._public_rpm, "queued": 0}
        )
        return {
            "state": self._state,
            "url": self._url,
            "allowed_count": len(self._allowed_models),
            "error": self._error,
            **rates,
        }

    def snapshot(self) -> dict:
        with self._lock:
            return self._snapshot_locked()

    @staticmethod
    def _spawn_ssh(
        executable: str,
        identity: str,
        known_hosts: str,
        gateway: TunnelGateway,
        remote_port: int,
    ):
        command = [
            *_ssh_base_command(executable, identity, known_hosts),
            "-N",
            "-R",
            f"127.0.0.1:{remote_port}:127.0.0.1:{gateway.port}",
            SSH_DESTINATION,
        ]
        return subprocess.Popen(
            command,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            env=_child_environment(),
            creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
            shell=False,
        )

    def _reconcile_gateway(self, gateway: TunnelGateway):
        with self._policy_lock:
            with self._lock:
                if not self._allowed_models:
                    raise ValueError
                policy = (
                    self._token,
                    self._selected_models,
                    self._marker_history,
                    self._secret_marker_history,
                    self._allowed_models,
                    self._public_rpm,
                    self._publisher_profile,
                )
                gateway.configure(
                    token=policy[0],
                    allowed_models=policy[1],
                    sensitive_markers=policy[2],
                    secret_markers=policy[3],
                    public_rpm=policy[5],
                )
                return policy

    def _publish_running(
        self,
        gateway,
        gateway_thread,
        process,
        url,
        expected_policy,
    ):
        with self._policy_lock:
            with self._lock:
                policy = (
                    self._token,
                    self._selected_models,
                    self._marker_history,
                    self._secret_marker_history,
                    self._allowed_models,
                    self._public_rpm,
                    self._publisher_profile,
                )
                if policy != expected_policy:
                    return None
                if not self._allowed_models or process.poll() is not None:
                    raise ValueError
                gateway.activate(
                    token=self._token,
                    allowed_models=self._selected_models,
                    sensitive_markers=self._marker_history,
                    secret_markers=self._secret_marker_history,
                    public_rpm=self._public_rpm,
                )
                self._gateway = gateway
                self._gateway_thread = gateway_thread
                self._process = process
                self._state, self._url, self._error = "running", url, ""
                return self._snapshot_locked()

    def start(self) -> dict:
        with self._operation_lock:
            with self._policy_lock:
                with self._lock:
                    if (
                        self._state == "running"
                        and self._process is not None
                        and self._process.poll() is None
                    ):
                        return self._snapshot_locked()
                    if not self._allowed_models:
                        raise ValueError("Select at least one tunnel model")
                    stale = (self._gateway, self._gateway_thread, self._process)
                    self._gateway = self._gateway_thread = self._process = None
                    token = self._token
                    models = self._selected_models
                    markers = self._marker_history
                    secret_markers = self._secret_marker_history
                    public_rpm = self._public_rpm
                    publisher_profile = self._publisher_profile
                    remote_port, _slug = parse_publisher_profile(publisher_profile)
                    public_url = publisher_url(publisher_profile)
                    self._state, self._url, self._error = "starting", "", ""
            self._cleanup(*stale)
            executable = _find_ssh()
            identity = _find_ssh_identity()
            if not executable:
                return self._start_failed("Tunnel SSH client is not installed")
            if not identity:
                return self._start_failed("Tunnel publisher key is not installed")
            try:
                known_hosts = _ensure_ssh_known_hosts(identity)
            except OSError:
                return self._start_failed("Tunnel host trust could not be prepared")
            readiness_token = secrets.token_urlsafe(32)
            gateway = TunnelGateway(
                self.relay_address,
                token,
                models,
                sensitive_markers=markers,
                secret_markers=secret_markers,
                route_guard=self._route_guard,
                policy_lock=self._policy_lock,
                start_inactive=True,
                readiness_token=readiness_token,
                public_rpm=public_rpm,
            )
            gateway_thread = threading.Thread(
                target=gateway.serve_forever,
                kwargs={"poll_interval": 0.1},
                name="tunnel-gateway",
                daemon=True,
            )
            gateway_thread.start()
            process = None
            result = None
            deadline = time.monotonic() + self._startup_timeout
            try:
                while time.monotonic() < deadline:
                    process = self._spawn_ssh(
                        executable, identity, known_hosts, gateway, remote_port
                    )
                    ready_at = min(
                        deadline,
                        time.monotonic() + self._readiness_delay,
                    )
                    while (
                        time.monotonic() < ready_at
                        and process.poll() is None
                    ):
                        time.sleep(min(0.05, ready_at - time.monotonic()))
                    while (
                        time.monotonic() < deadline
                        and process.poll() is None
                    ):
                        policy = self._reconcile_gateway(gateway)
                        remaining = deadline - time.monotonic()
                        if _https_models_ready(
                            public_url,
                            policy[0],
                            readiness_token,
                            policy[4],
                            min(READINESS_PROBE_TIMEOUT, remaining),
                        ):
                            result = self._publish_running(
                                gateway,
                                gateway_thread,
                                process,
                                public_url,
                                policy,
                            )
                            if result is not None:
                                break
                        time.sleep(min(0.1, max(0.0, deadline - time.monotonic())))
                    if result is not None or process.poll() != 255:
                        break
                    process = None
                    time.sleep(min(0.2, max(0.0, deadline - time.monotonic())))
                if result is None:
                    raise RuntimeError
            except Exception:
                self._cleanup(gateway, gateway_thread, process)
                return self._start_failed("Tunnel could not start")
            threading.Thread(
                target=self._monitor,
                args=(process,),
                name="tunnel-process-monitor",
                daemon=True,
            ).start()
            return result

    def _start_failed(self, message: str):
        with self._lock:
            self._state, self._url, self._error = "error", "", message
        raise RuntimeError(message)

    def _monitor(self, process) -> None:
        process.wait()
        with self._operation_lock:
            with self._lock:
                if process is not self._process or self._state in {"stopped", "stopping"}:
                    return
                gateway, gateway_thread = self._gateway, self._gateway_thread
                self._gateway = self._gateway_thread = self._process = None
                self._state, self._url, self._error = (
                    "error",
                    "",
                    "Tunnel connection stopped",
                )
            self._cleanup(gateway, gateway_thread, process)

    @staticmethod
    def _cleanup(gateway, gateway_thread, process) -> None:
        if process is not None and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=3)
        if gateway is not None:
            gateway.shutdown()
            gateway.server_close()
        if gateway_thread is not None:
            gateway_thread.join(timeout=2)
        output = getattr(process, "stdout", None)
        if output is not None:
            output.close()

    def stop(self) -> dict:
        with self._operation_lock:
            with self._lock:
                if self._state == "stopped":
                    return self._snapshot_locked()
                self._state = "stopping"
                gateway, gateway_thread, process = (
                    self._gateway,
                    self._gateway_thread,
                    self._process,
                )
                self._gateway = self._gateway_thread = self._process = None
            self._cleanup(gateway, gateway_thread, process)
            with self._lock:
                self._state, self._url, self._error = "stopped", "", ""
                return self._snapshot_locked()

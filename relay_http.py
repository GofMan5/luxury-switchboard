"""HTTP transport for the switchable local provider relay."""

from __future__ import annotations

import base64
import http.client
import ipaddress
import json
import math
import os
import secrets
import select
import socket
import sqlite3
import ssl
import tempfile
import threading
import time
import unicodedata
from dataclasses import replace
from email.utils import parsedate_to_datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import unquote, urlsplit

from relay_config import ConfigError, ConfigStore
from relay_framing import (
    BodyTooLarge,
    InvalidBodyFraming,
    body_framing,
    media_type,
    parse_multipart,
    read_body,
)
from relay_history import HistoryStore, TunnelHistoryStore
from relay_image_compat import (
    MAX_IMAGE_RESPONSE_BYTES,
    InvalidImageRequest,
    InvalidImageResponse,
    images_response,
    prepare_image_request,
)
from relay_tunnel import (
    CONTROL_SELF_NAME,
    DEFAULT_PUBLISHER_PROFILE,
    TUNNEL_MODEL_HEADER,
    InvalidJson,
    SharedTunnelControl,
    TunnelController,
    _load_json,
    model_route_token,
    parse_publisher_profile,
    validate_context_limit_kib,
)
from relay_runtime import (
    ClientDisconnected,
    ProviderRegistry,
    RelayMetrics,
    RelayStopping,
    TokenUsage,
    key_fingerprint,
    normalize_api_key,
)


LISTEN = ("127.0.0.1", 8798)
UPSTREAM_TIMEOUT = 300
STREAM_HEADER_TIMEOUT = 45
STREAM_IDLE_TIMEOUT = 180
STREAM_HEARTBEAT_INTERVAL = 15
SELECT_SOCKET_LIMIT = 512
MAX_REQUEST_BYTES = 64 * 1024 * 1024
MAX_INSPECT_BYTES = 4 * 1024 * 1024
MAX_ERROR_BYTES = 64 * 1024
ERROR_BODY_TIMEOUT = 2.0
RESPONSE_SPOOL_BYTES = 2 * 1024 * 1024
MAX_BUFFERED_RESPONSE_BYTES = 256 * 1024 * 1024
MAX_SSE_EVENT_BYTES = 64 * 1024 * 1024
TUNNEL_REQUEST_HEADER = "X-Provider-Switch-Tunnel"
MAX_TUNNEL_MODELS = 256
TUNNEL_MODEL_PROBE_TIMEOUT = 10.0
MAX_TUNNEL_MODEL_PROBES = 4
TUNNEL_MODEL_PROBE_STATES = frozenset(
    {"testing", "available", "unavailable", "timeout"}
)
SSE_TERMINAL_EVENTS = frozenset(
    {
        "response.completed",
        "response.failed",
        "response.incomplete",
        "image_generation.completed",
        "image_edit.completed",
    }
)
HOP_HEADERS = {
    "connection",
    "expect",
    "host",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "proxy-connection",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
}
REWRITTEN_RESPONSE_HEADERS = {
    "accept-ranges",
    "content-encoding",
    "content-digest",
    "content-location",
    "content-md5",
    "content-range",
    "content-type",
    "digest",
    "etag",
    "last-modified",
    "repr-digest",
    "signature",
    "signature-input",
    "vary",
}
REWRITTEN_REQUEST_HEADERS = {
    "accept",
    "content-digest",
    "content-encoding",
    "content-md5",
    "content-type",
    "digest",
    "repr-digest",
    "signature",
    "signature-input",
}
TLS_CONTEXT = ssl.create_default_context()
_GENERIC_HOST_LABELS = frozenset(
    {
        "api",
        "app",
        "cloud",
        "co",
        "com",
        "dev",
        "gateway",
        "io",
        "local",
        "localhost",
        "net",
        "one",
        "org",
        "proxy",
        "ru",
        "uk",
        "v1",
        "www",
    }
)


def _hostname_privacy_markers(hostname: str) -> set[str]:
    host = hostname.strip().rstrip(".")
    if not host:
        return set()
    address_text, separator, scope = host.partition("%")
    try:
        address = ipaddress.ip_address(address_text)
    except ValueError:
        pass
    else:
        markers = {host, str(address), address.exploded}
        if separator:
            scopes = {scope, scope[2:] if scope.casefold().startswith("25") else scope}
            markers.update(
                f"{value}%{item}"
                for value in (str(address), address.exploded)
                for item in scopes
                if item
            )
        return markers

    hosts = {host}
    try:
        hosts.add(host.encode("idna").decode("ascii"))
    except UnicodeError:
        pass
    for value in tuple(hosts):
        try:
            hosts.add(value.encode("ascii").decode("idna"))
        except UnicodeError:
            pass

    markers = set(hosts)
    for value in hosts:
        labels = unicodedata.normalize("NFKC", value).rstrip(".").split(".")
        markers.update(
            label
            for label in labels[:-1]
            if label and label.casefold() not in _GENERIC_HOST_LABELS
        )
    return markers


def environment_api_key() -> str:
    value = os.environ.get("FREEMODEL_API_KEY", "")
    if value or os.name != "nt":
        return value
    try:
        import winreg

        with winreg.OpenKey(winreg.HKEY_CURRENT_USER, "Environment") as key:
            value, _kind = winreg.QueryValueEx(key, "FREEMODEL_API_KEY")
    except (ImportError, FileNotFoundError, OSError):
        return ""
    return value if isinstance(value, str) else ""


def _tunnel_models(values) -> tuple[str, ...]:
    if values is None:
        return ()
    if not isinstance(values, (list, tuple, set, frozenset)):
        raise ValueError("Tunnel model selection is invalid")
    models = []
    for value in values:
        if (
            not isinstance(value, str)
            or value != value.strip()
            or not value
            or len(value) > 128
            or not value.isprintable()
        ):
            raise ValueError("Tunnel model selection is invalid")
        if value not in models:
            models.append(value)
        if len(models) > MAX_TUNNEL_MODELS:
            raise ValueError("Too many tunnel models selected")
    return tuple(models)


def _tunnel_catalog_models(value) -> tuple[str, ...]:
    if isinstance(value, dict):
        value = value.get("data", value.get("models", ()))
    if isinstance(value, (str, bytes)) or value is None:
        value = (value,) if isinstance(value, str) else ()
    if not isinstance(value, (list, tuple, set, frozenset)):
        raise ValueError("Provider returned an invalid model catalog")
    models = []
    for item in value:
        if isinstance(item, str):
            model = item
        elif isinstance(item, dict):
            model = item.get("id") or item.get("name") or ""
        else:
            model = getattr(item, "id", None) or getattr(item, "name", "")
        if model:
            models.append(model)
    return _tunnel_models(models)


def _tunnel_routes(value, models, default_provider_id: str) -> dict[str, str]:
    if value is None:
        return {model: default_provider_id for model in models}
    if not isinstance(value, dict) or len(value) > MAX_TUNNEL_MODELS:
        raise ValueError("Saved tunnel settings are invalid")
    selected = _tunnel_models(tuple(value))
    ordered = tuple(model for model in models if model in value) + tuple(
        model for model in selected if model not in models
    )
    routes = {}
    for model in ordered:
        provider_id = value[model]
        if (
            not isinstance(provider_id, str)
            or not provider_id
            or len(provider_id) > 64
            or any(
                not (character.isalnum() or character in "-_")
                for character in provider_id
            )
        ):
            raise ValueError("Saved tunnel settings are invalid")
        routes[model] = provider_id
    return routes


def _tunnel_settings(
    saved: dict | None,
    default_provider_id: str,
) -> tuple[tuple[str, ...], dict[str, str], str, int, int, str, str]:
    raw = saved.get("tunnel", {}) if saved else {}
    if not isinstance(raw, dict):
        raise ValueError("Saved tunnel settings are invalid")
    token = raw.get("access_token", "")
    if not isinstance(token, str) or (
        token
        and (
            len(token) < 32
            or len(token) > 512
            or token != token.strip()
            or not token.isprintable()
        )
    ):
        raise ValueError("Saved tunnel settings are invalid")
    rpm_per_ip = raw.get("rpm_per_ip", 0)
    if (
        not isinstance(rpm_per_ip, int)
        or isinstance(rpm_per_ip, bool)
        or rpm_per_ip < 0
    ):
        raise ValueError("Saved tunnel settings are invalid")
    try:
        context_limit_kib = validate_context_limit_kib(
            raw.get("context_limit_kib", 0)
        )
    except ValueError as error:
        raise ValueError("Saved tunnel settings are invalid") from error
    publisher_profile = raw.get(
        "publisher_profile", DEFAULT_PUBLISHER_PROFILE
    )
    try:
        parse_publisher_profile(publisher_profile)
    except ValueError as error:
        raise ValueError("Saved tunnel settings are invalid") from error
    provider_id = raw.get("provider_id", default_provider_id)
    if not isinstance(provider_id, str) or not provider_id:
        raise ValueError("Saved tunnel settings are invalid")
    allowed_models = _tunnel_models(raw.get("allowed_models", ()))
    model_routes = _tunnel_routes(
        raw.get("model_routes"), allowed_models, provider_id
    )
    return (
        tuple(model_routes),
        model_routes,
        token,
        rpm_per_ip,
        context_limit_kib,
        publisher_profile,
        provider_id,
    )


def upstream_target(upstream, request_target: str) -> str:
    request = urlsplit(request_target)
    path = request.path or "/"
    base_path = upstream.path.rstrip("/")
    if base_path and path != base_path and not path.startswith(base_path + "/"):
        path = base_path + "/" + path.lstrip("/")
    return path + (("?" + request.query) if request.query else "")


def json_object(body: bytes | None):
    try:
        payload = json.loads(body)
    except (json.JSONDecodeError, UnicodeDecodeError, RecursionError, TypeError):
        return None
    return payload if isinstance(payload, dict) else None


def request_model(payload) -> str:
    model = payload.get("model") if payload else None
    if not isinstance(model, str):
        return "—"
    model = "".join(character for character in model if character.isprintable()).strip()
    return model[:64] or "—"


def request_uses_images(payload, request_target: str) -> bool:
    if urlsplit(request_target).path.startswith("/v1/images/"):
        return True
    pending = [payload]
    while pending:
        value = pending.pop()
        if isinstance(value, dict):
            if value.get("type") in {"image_generation", "input_image"}:
                return True
            pending.extend(value.values())
        elif isinstance(value, list):
            pending.extend(value)
    return False


def multipart_routing_payload(content_type: str, body: bytes) -> dict | None:
    try:
        form = parse_multipart(content_type, body)
    except InvalidBodyFraming:
        return None
    models = form.values("model")
    streams = form.values("stream")
    if len(models) != 1 or len(streams) > 1:
        return None
    model = models[0]
    if (
        model != model.strip()
        or not model
        or len(model) > 256
        or not model.isprintable()
    ):
        return None
    payload = {"model": model}
    if streams:
        if streams[0] != streams[0].strip():
            return None
        stream = streams[0]
        if stream not in {"true", "false"}:
            return None
        payload["stream"] = stream == "true"
    return payload


def model_unavailable_on_plan(status: int, body: bytes, model: str) -> bool:
    if status != 404 or not body or model == "—":
        return False
    text = body.decode("utf-8", "ignore").casefold()
    return f"model '{model}' is not available on your plan".casefold() in text


def balance_exhausted(status: int, body: bytes) -> bool:
    if status == 402:
        return True
    if not 400 <= status < 500 or not body:
        return False
    text = body.decode("utf-8", "ignore").casefold().replace("_", " ").replace("-", " ")
    return any(
        marker in text
        for marker in (
            "insufficient balance",
            "insufficient funds",
            "insufficient quota",
            "not enough balance",
            "balance is too low",
            "balance too low",
            "balance exhausted",
            "\u043d\u0435\u0434\u043e\u0441\u0442\u0430\u0442\u043e\u0447\u043d\u043e \u0441\u0440\u0435\u0434\u0441\u0442\u0432",
            "\u043d\u0435\u0434\u043e\u0441\u0442\u0430\u0442\u043e\u0447\u043d\u044b\u0439 \u0431\u0430\u043b\u0430\u043d\u0441",
            "\u0431\u0430\u043b\u0430\u043d\u0441 \u0438\u0441\u0447\u0435\u0440\u043f\u0430\u043d",
        )
    )


def error_response_body(response, connection) -> bytes:
    data = bytearray()
    sock = getattr(connection, "sock", None)
    previous_timeout = sock.gettimeout() if sock is not None else None
    deadline = time.monotonic() + ERROR_BODY_TIMEOUT
    try:
        while len(data) < MAX_ERROR_BYTES:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                break
            if sock is not None:
                sock.settimeout(
                    min(previous_timeout, remaining)
                    if previous_timeout is not None
                    else remaining
                )
            chunk = response.read1(MAX_ERROR_BYTES - len(data))
            if not chunk:
                break
            data.extend(chunk)
            if response.length == 0:
                break
    except (OSError, http.client.HTTPException):
        pass
    finally:
        if sock is not None:
            try:
                sock.settimeout(previous_timeout)
            except OSError:
                pass
    return bytes(data)


def upstream_connection(upstream, target: str, proxy_url: str = "", timeout=UPSTREAM_TIMEOUT):
    if not proxy_url:
        connection_class = (
            http.client.HTTPSConnection
            if upstream.scheme == "https"
            else http.client.HTTPConnection
        )
        kwargs = {"timeout": timeout}
        if upstream.scheme == "https":
            kwargs["context"] = TLS_CONTEXT
        return connection_class(upstream.hostname, upstream.port, **kwargs), target, {}

    proxy = urlsplit(proxy_url)
    authorization = None
    if proxy.username is not None:
        credentials = f"{unquote(proxy.username)}:{unquote(proxy.password or '')}"
        authorization = "Basic " + base64.b64encode(credentials.encode()).decode()
    if upstream.scheme == "https":
        connection = http.client.HTTPSConnection(
            proxy.hostname, proxy.port or 80, timeout=timeout, context=TLS_CONTEXT
        )
        headers = {"Proxy-Authorization": authorization} if authorization else None
        connection.set_tunnel(upstream.hostname, upstream.port or 443, headers=headers)
        return connection, target, {}

    connection = http.client.HTTPConnection(proxy.hostname, proxy.port or 80, timeout=timeout)
    headers = {"Proxy-Authorization": authorization} if authorization else {}
    absolute_target = f"{upstream.scheme}://{upstream.netloc}{target}"
    return connection, absolute_target, headers


def install_cancelable_connect(connection, register_socket, check_cancelled) -> None:
    def create_connection(
        address,
        timeout=socket._GLOBAL_DEFAULT_TIMEOUT,
        source_address=None,
    ):
        resolved = []
        errors = []
        done = threading.Event()

        for family in (socket.AF_INET, socket.AF_INET6):
            try:
                socket.inet_pton(family, address[0])
            except OSError:
                continue
            socket_address = (
                (address[0], address[1])
                if family == socket.AF_INET
                else (address[0], address[1], 0, 0)
            )
            resolved.append((family, socket.SOCK_STREAM, 0, "", socket_address))
            done.set()
            break

        def resolve():
            try:
                resolved.extend(
                    socket.getaddrinfo(address[0], address[1], 0, socket.SOCK_STREAM)
                )
            except BaseException as error:
                errors.append(error)
            finally:
                done.set()

        if not done.is_set():
            # ponytail: one daemon per in-flight DNS lookup; add single-flight
            # only if configured resolvers measurably create thread pressure.
            threading.Thread(target=resolve, name="relay-dns", daemon=True).start()
        while not done.wait(0.05):
            check_cancelled()
        check_cancelled()
        if errors:
            raise errors[0]
        if not resolved:
            raise OSError("Provider address could not be resolved")

        last_error = None
        for family, sock_type, protocol, _canonname, socket_address in resolved:
            check_cancelled()
            sock = socket.socket(family, sock_type, protocol)
            try:
                if timeout is not socket._GLOBAL_DEFAULT_TIMEOUT:
                    sock.settimeout(timeout)
                if source_address:
                    sock.bind(source_address)
                connection.sock = sock
                register_socket(connection, sock)
                sock.connect(socket_address)
                check_cancelled()
                return sock
            except OSError as error:
                last_error = error
                if connection.sock is sock:
                    connection.sock = None
                sock.close()
                check_cancelled()
            except BaseException:
                if connection.sock is sock:
                    connection.sock = None
                sock.close()
                raise
        raise last_error or OSError("Provider connection failed")

    connection._create_connection = create_connection
    if isinstance(connection, http.client.HTTPSConnection):
        def connect_https():
            http.client.HTTPConnection.connect(connection)
            check_cancelled()
            server_hostname = connection._tunnel_host or connection.host
            ssl_sock = connection._context.wrap_socket(
                connection.sock,
                server_hostname=server_hostname,
                do_handshake_on_connect=False,
            )
            connection.sock = ssl_sock
            register_socket(connection, ssl_sock)
            check_cancelled()
            try:
                ssl_sock.do_handshake()
            except (OSError, AttributeError) as error:
                try:
                    check_cancelled()
                except (ClientDisconnected, RelayStopping) as cancelled:
                    raise cancelled from error
                raise OSError("TLS handshake failed") from error
            check_cancelled()

        connection.connect = connect_https


def _abort_socket(sock) -> None:
    if sock is not None:
        try:
            sock.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        try:
            socket.close(sock.detach())
        except OSError:
            pass


def _abort_upstream(connection, sock=None) -> None:
    sock = sock if sock is not None else getattr(connection, "sock", None)
    _abort_socket(sock)
    connection.close()


def echo_cache_for_one_hour(body: bytes, payload=None) -> bytes:
    payload = payload if isinstance(payload, dict) else json_object(body)
    if payload is None:
        return body
    changed = False
    candidates = [payload]

    def add_content_blocks(content):
        pending = [content] if isinstance(content, dict) else list(content) if isinstance(content, list) else []
        while pending:
            block = pending.pop()
            if not isinstance(block, dict):
                continue
            candidates.append(block)
            if block.get("type") == "tool_result":
                nested = block.get("content")
                if isinstance(nested, dict):
                    pending.append(nested)
                elif isinstance(nested, list):
                    pending.extend(nested)

    tools = payload.get("tools")
    if isinstance(tools, list):
        candidates.extend(tool for tool in tools if isinstance(tool, dict))
    add_content_blocks(payload.get("system"))
    for field in ("messages", "input"):
        entries = payload.get(field)
        if not isinstance(entries, list):
            continue
        for entry in entries:
            if isinstance(entry, dict):
                candidates.append(entry)
                add_content_blocks(entry.get("content"))
    for value in candidates:
        cache_control = value.get("cache_control")
        if (
            isinstance(cache_control, dict)
            and cache_control.get("type") == "ephemeral"
            and cache_control.get("ttl") != "1h"
        ):
            cache_control["ttl"] = "1h"
            changed = True
    return (
        json.dumps(payload, separators=(",", ":")).encode("utf-8")
        if changed
        else body
    )


def retry_after_seconds(value: str | None, retry_number: int) -> float:
    if value:
        try:
            delay = float(value)
        except ValueError:
            try:
                delay = parsedate_to_datetime(value).timestamp() - time.time()
            except (TypeError, ValueError, OverflowError):
                delay = 0
        if math.isfinite(delay) and delay > 0:
            return delay
    return min(60.0, 2 ** min(max(0, retry_number - 1), 6))


def key_wide_failure(status: int | None) -> bool:
    return status == 429


def _positive_int(value) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) and value > 0 else 0


class ResponseInspector:
    """Extract token usage without persisting response content."""

    def __init__(
        self,
        content_type: str,
        max_sse_event_bytes: int = MAX_INSPECT_BYTES,
    ):
        self._sse = "text/event-stream" in content_type.lower()
        self._json = "json" in content_type.lower()
        self._max_sse_event_bytes = max_sse_event_bytes
        self._buffer = bytearray()
        self._discard_sse_line = False
        self._sse_data = bytearray()
        self._discard_sse_event = False
        self._usage = TokenUsage()
        self._terminal_at: float | None = None
        self._terminal_event = ""

    def feed(self, chunk: bytes) -> int:
        if not self._sse and not self._json:
            return len(chunk)
        if self._sse:
            return self._feed_sse(chunk)
        self._buffer.extend(chunk[: MAX_INSPECT_BYTES - len(self._buffer)])
        return len(chunk)

    def _feed_sse(self, chunk: bytes) -> int:
        offset = 0
        while offset < len(chunk):
            if self._discard_sse_line:
                newline = chunk.find(b"\n", offset)
                if newline < 0:
                    return len(chunk)
                self._discard_sse_line = False
                offset = newline + 1
                continue

            newline = chunk.find(b"\n", offset)
            end = len(chunk) if newline < 0 else newline + 1
            fragment = chunk[offset:end]
            available = self._max_sse_event_bytes - len(self._buffer)
            if len(fragment) > available:
                self._buffer.clear()
                self._discard_sse_event = True
                if newline < 0:
                    self._discard_sse_line = True
                    return len(chunk)
                offset = end
                continue
            self._buffer.extend(fragment)
            if newline < 0:
                return len(chunk)
            if self._consume_sse_lines():
                return end
            offset = end
        return len(chunk)

    @property
    def usage(self) -> TokenUsage:
        return self._usage

    @property
    def terminal_at(self) -> float | None:
        return self._terminal_at

    @property
    def terminal_event(self) -> str:
        return self._terminal_event

    def _record_terminal(self, event_type: str) -> bool:
        terminal = isinstance(event_type, str) and event_type in SSE_TERMINAL_EVENTS
        if terminal and not self._terminal_event:
            self._terminal_event = event_type
            if self._sse and self._terminal_at is None:
                self._terminal_at = time.monotonic()
        return terminal

    def _consume_sse_lines(self, final: bool = False) -> bool:
        while True:
            index = self._buffer.find(b"\n")
            if index < 0:
                if final and self._buffer:
                    line, self._buffer = bytes(self._buffer), bytearray()
                else:
                    return False
            else:
                line = bytes(self._buffer[:index])
                del self._buffer[: index + 1]
            line = line.rstrip(b"\r")
            if not line:
                if self._finish_sse_event():
                    return True
                continue
            if self._discard_sse_event or not line.startswith(b"data:"):
                continue
            data = line[5:]
            if data.startswith(b" "):
                data = data[1:]
            extra = len(data) + bool(self._sse_data)
            if extra > self._max_sse_event_bytes - len(self._sse_data):
                self._sse_data.clear()
                self._discard_sse_event = True
                continue
            if self._sse_data:
                self._sse_data.extend(b"\n")
            self._sse_data.extend(data)
            if final and index < 0:
                return self._finish_sse_event()

    def _finish_sse_event(self) -> bool:
        if self._discard_sse_event:
            self._sse_data.clear()
            self._discard_sse_event = False
            return False
        if not self._sse_data:
            return False
        data = bytes(self._sse_data)
        self._sse_data.clear()
        if data == b"[DONE]":
            return False
        try:
            payload = _load_json(data)
        except InvalidJson:
            return False
        terminal_before = self._terminal_event
        self._merge_payload(payload)
        return not terminal_before and bool(self._terminal_event)

    def _merge_payload(self, payload) -> None:
        if not isinstance(payload, dict):
            return
        candidates = [payload]
        for field in ("response", "message", "delta"):
            value = payload.get(field)
            if isinstance(value, dict):
                candidates.append(value)
        merged_usage = False
        for candidate in candidates:
            usage = candidate.get("usage")
            if not isinstance(usage, dict):
                continue
            merged_usage = True
            input_tokens = _positive_int(
                usage.get("input_tokens", usage.get("prompt_tokens"))
            )
            output_tokens = _positive_int(
                usage.get("output_tokens", usage.get("completion_tokens"))
            )
            input_details = usage.get("input_tokens_details") or usage.get(
                "prompt_tokens_details"
            )
            output_details = usage.get("output_tokens_details") or usage.get(
                "completion_tokens_details"
            )
            cached_tokens = _positive_int(usage.get("cached_tokens"))
            reasoning_tokens = _positive_int(usage.get("reasoning_tokens"))
            anthropic_cached_tokens = (
                _positive_int(usage.get("cache_read_input_tokens"))
                + _positive_int(usage.get("cache_creation_input_tokens"))
            )
            if isinstance(input_details, dict):
                cached_tokens = max(
                    cached_tokens, _positive_int(input_details.get("cached_tokens"))
                )
            if isinstance(output_details, dict):
                reasoning_tokens = max(
                    reasoning_tokens,
                    _positive_int(output_details.get("reasoning_tokens")),
                )
            cached_tokens = max(
                cached_tokens,
                anthropic_cached_tokens,
            )
            merged_input = max(self._usage.input_tokens, input_tokens)
            merged_output = max(self._usage.output_tokens, output_tokens)
            context_tokens = max(
                self._usage.context_tokens,
                input_tokens + anthropic_cached_tokens
                if anthropic_cached_tokens
                else input_tokens,
            )
            total_tokens = max(
                self._usage.total_tokens,
                _positive_int(usage.get("total_tokens")),
                context_tokens + merged_output,
            )
            self._usage = TokenUsage(
                input_tokens=merged_input,
                output_tokens=merged_output,
                cached_tokens=max(self._usage.cached_tokens, cached_tokens),
                reasoning_tokens=max(self._usage.reasoning_tokens, reasoning_tokens),
                total_tokens=total_tokens,
                context_tokens=context_tokens,
            )
        event_type = payload.get("type")
        if event_type == "response.completed" and not completed_response(
            payload.get("response")
        ):
            event_type = "response.incomplete"
        self._record_terminal(event_type)
        if (
            self._sse
            and merged_usage
            and event_type == "message_delta"
            and self._terminal_at is None
        ):
            self._terminal_at = time.monotonic()

    def finish(self) -> TokenUsage:
        if self._sse:
            self._consume_sse_lines(final=True)
            self._finish_sse_event()
        elif self._json and self._buffer:
            try:
                self._merge_payload(json.loads(self._buffer))
            except (json.JSONDecodeError, UnicodeDecodeError, RecursionError):
                pass
        if self._usage.output_tokens and self._terminal_at is None:
            self._terminal_at = time.monotonic()
        self._buffer.clear()
        return self._usage


def http_error_detail(status: int) -> str:
    if status in {401, 403}:
        return "Provider rejected authentication"
    if status == 404:
        return "Provider route or model was not found"
    if status in {400, 409, 422}:
        return "Provider rejected the request"
    if status == 408:
        return "Provider request timed out"
    if status == 413:
        return "Provider rejected the request size"
    if status in {429, 529}:
        return "Provider is rate limited or overloaded"
    if status >= 500:
        return "Provider returned a server error"
    return f"Provider returned HTTP {status}"


def network_error_detail(error: Exception) -> str:
    if isinstance(error, (TimeoutError, socket.timeout)):
        return "Provider did not respond before the timeout"
    if isinstance(error, socket.gaierror):
        return "Provider hostname could not be resolved"
    if isinstance(error, ConnectionRefusedError):
        return "Provider refused the connection"
    if isinstance(error, ConnectionResetError):
        return "Provider reset the connection"
    if isinstance(error, ssl.SSLError):
        return "Provider TLS handshake failed"
    if isinstance(error, (IncompleteSSE, http.client.IncompleteRead)):
        return "Provider closed the response early"
    if isinstance(error, http.client.HTTPException):
        return "Provider returned an invalid HTTP response"
    return "Provider connection failed"


class IncompleteSSE(http.client.HTTPException):
    pass


def completed_response(value) -> bool:
    return (
        isinstance(value, dict)
        and value.get("error") is None
        and value.get("status") == "completed"
        and value.get("incomplete_details") is None
        and isinstance(value.get("output"), list)
    )


def buffered_response(
    response,
    expected_length: int | None,
    required_terminal: str = "",
    max_bytes: int = MAX_BUFFERED_RESPONSE_BYTES,
    check_cancelled=None,
):
    content_encoding = response.getheader("Content-Encoding")
    if (
        required_terminal
        and content_encoding
        and content_encoding.strip().casefold() != "identity"
    ):
        raise http.client.HTTPException("Encoded event stream")
    if (
        expected_length is not None
        and expected_length > max_bytes
    ):
        raise http.client.HTTPException("Provider response is too large")
    spool = tempfile.SpooledTemporaryFile(max_size=RESPONSE_SPOOL_BYTES, mode="w+b")
    inspector = (
        ResponseInspector(
            response.getheader("Content-Type") or "",
            MAX_SSE_EVENT_BYTES,
        )
        if required_terminal
        else None
    )
    total = 0
    try:
        while True:
            if check_cancelled is not None:
                check_cancelled()
            chunk = response.read1(64 * 1024)
            if not chunk:
                break
            if len(chunk) > max_bytes - total:
                raise http.client.HTTPException("Provider response is too large")
            consumed = inspector.feed(chunk) if inspector is not None else len(chunk)
            spool.write(chunk[:consumed])
            total += consumed
            if check_cancelled is not None:
                check_cancelled()
            if inspector is not None and inspector.terminal_event:
                break
        if (
            expected_length is not None
            and total != expected_length
            and (inspector is None or inspector.terminal_event != required_terminal)
        ):
            if required_terminal:
                raise IncompleteSSE("Provider closed the event stream early")
            raise http.client.IncompleteRead(b"", expected_length - total)
        if inspector is not None:
            inspector.finish()
        if inspector is not None and inspector.terminal_event != required_terminal:
            raise IncompleteSSE("Provider omitted response.completed")
        spool.seek(0)
        return spool, total
    except BaseException as error:
        spool.close()
        if required_terminal and isinstance(
            error, (OSError, http.client.IncompleteRead)
        ):
            raise IncompleteSSE("Provider closed the event stream early") from error
        raise


def completed_sse_response(source):
    try:
        raw = source.read(MAX_BUFFERED_RESPONSE_BYTES + 1)
        if len(raw) > MAX_BUFFERED_RESPONSE_BYTES:
            raise http.client.HTTPException("Provider response is too large")
        value = _load_json(raw)
    except InvalidJson as error:
        raise http.client.HTTPException("Invalid non-stream response") from error
    finally:
        source.close()
    if not completed_response(value):
        raise http.client.HTTPException("Incomplete non-stream response")
    body = (
        b"data: "
        + json.dumps(
            {"type": "response.completed", "response": value},
            separators=(",", ":"),
            allow_nan=False,
        ).encode("utf-8")
        + b"\n\n"
    )
    if len(body) > MAX_BUFFERED_RESPONSE_BYTES:
        raise http.client.HTTPException("Provider response is too large")
    spool = tempfile.SpooledTemporaryFile(max_size=RESPONSE_SPOOL_BYTES, mode="w+b")
    spool.write(body)
    spool.seek(0)
    return spool, len(body)


class _RequestBodyReader:
    def __init__(self, stream, sock, check_cancelled):
        self._stream = stream
        self._sock = sock
        self._check_cancelled = check_cancelled
        self._buffer = bytearray()
        self._eof = False

    def _fill(self) -> bool:
        while not self._eof:
            self._check_cancelled()
            try:
                chunk = self._stream.read1(64 * 1024)
            except (BlockingIOError, InterruptedError):
                chunk = None
            if chunk:
                self._buffer.extend(chunk)
                return True
            self._check_cancelled()
            try:
                readable, _, _ = select.select([self._sock], [], [], 0.25)
            except (OSError, ValueError):
                readable = (self._sock,)
            if not readable:
                continue
            try:
                if self._sock.recv(1, socket.MSG_PEEK) == b"":
                    self._eof = True
            except BlockingIOError:
                continue
        return False

    def read(self, size: int) -> bytes:
        self._check_cancelled()
        while len(self._buffer) < size and self._fill():
            pass
        result = bytes(self._buffer[:size])
        del self._buffer[:size]
        return result

    def readline(self, limit: int) -> bytes:
        self._check_cancelled()
        while True:
            newline = self._buffer.find(b"\n", 0, limit)
            if newline >= 0:
                end = newline + 1
                break
            if len(self._buffer) >= limit or not self._fill():
                end = min(limit, len(self._buffer))
                break
        result = bytes(self._buffer[:end])
        del self._buffer[:end]
        return result


class RelayHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "ProviderSwitch/2.0"
    sys_version = ""

    def _body(self):
        try:
            framing = body_framing(self.headers, MAX_REQUEST_BYTES)
            previous_timeout = self.connection.gettimeout()

            def check_cancelled():
                if self.server.stopping.is_set():
                    raise RelayStopping
                cancel_event = getattr(self, "_local_cancel_event", None)
                if cancel_event is not None and cancel_event.is_set():
                    raise ClientDisconnected

            self.connection.setblocking(False)
            try:
                body = read_body(
                    _RequestBodyReader(
                        self.rfile,
                        self.connection,
                        check_cancelled,
                    ),
                    framing,
                    MAX_REQUEST_BYTES,
                )
            finally:
                self.connection.settimeout(previous_timeout)
        except BodyTooLarge:
            self._error(413, "Request body is too large")
            return False, None
        except InvalidBodyFraming:
            if self.server.stopping.is_set():
                raise RelayStopping
            cancel_event = getattr(self, "_local_cancel_event", None)
            if cancel_event is not None and cancel_event.is_set():
                raise ClientDisconnected
            self._error(400, "Invalid request framing")
            return False, None
        except OSError as error:
            if self.server.stopping.is_set():
                raise RelayStopping from error
            raise ClientDisconnected from error
        return True, body

    def _finish_client_headers(self) -> None:
        try:
            self.end_headers()
        except OSError as error:
            raise ClientDisconnected from error

    def _write_client(self, body: bytes) -> None:
        try:
            self.wfile.write(body)
            self.wfile.flush()
        except OSError as error:
            raise ClientDisconnected from error
        self._response_bytes += len(body)
        self.server.metrics.add_bytes(bytes_out=len(body))
        self.server.metrics.progress(self._request_id, self._response_bytes)

    def _client_disconnected(self) -> bool:
        cancel_event = getattr(self, "_local_cancel_event", None)
        if cancel_event is not None and cancel_event.is_set():
            return True
        heartbeat_at = getattr(self, "_sse_heartbeat_at", None)
        if heartbeat_at is not None and time.monotonic() >= heartbeat_at:
            try:
                self._write_client(b": keep-alive\n\n")
            except ClientDisconnected:
                return True
            self._sse_heartbeat_at = time.monotonic() + STREAM_HEARTBEAT_INTERVAL
        try:
            readable, _, _ = select.select([self.connection], [], [], 0)
            return bool(readable and self.connection.recv(1, socket.MSG_PEEK) == b"")
        except (OSError, ValueError):
            return True

    def _check_cancelled(self) -> None:
        if self.server.stopping.is_set():
            raise RelayStopping
        if self._client_disconnected():
            raise ClientDisconnected

    def _wait_retry(self, delay: float) -> None:
        deadline = time.monotonic() + delay
        while True:
            self._check_cancelled()
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return
            waiter = getattr(self, "_local_cancel_event", None)
            waiter = waiter if waiter is not None else self.server.stopping
            if waiter.wait(min(0.25, remaining)):
                if self.server.stopping.is_set():
                    raise RelayStopping
                raise ClientDisconnected

    def _request_headers(self, spec, target: str, configured_key: str | None) -> dict:
        blocked = HOP_HEADERS | {
            name.strip().lower()
            for name in self.headers.get("Connection", "").split(",")
            if name.strip()
        }
        blocked.add("content-length")
        blocked.add(TUNNEL_REQUEST_HEADER.lower())
        blocked.add(TUNNEL_MODEL_HEADER.lower())
        blocked.add("x-openai-actor-authorization")
        headers = {
            name: value
            for name, value in self.headers.items()
            if name.lower() not in blocked
        }
        if spec.auth_mode == "passthrough":
            return headers
        authorization = next(
            (value for name, value in headers.items() if name.lower() == "authorization"),
            None,
        )
        api_key = next(
            (value for name, value in headers.items() if name.lower() == "x-api-key"),
            None,
        )
        headers = {
            name: value
            for name, value in headers.items()
            if name.lower() not in {"authorization", "x-api-key"}
        }
        key = configured_key
        if key is None and api_key:
            key = api_key.strip()
        if key is None and authorization:
            scheme, separator, value = authorization.partition(" ")
            if separator and scheme.lower() == "bearer" and value.strip():
                key = value.strip()
        mode = spec.auth_mode
        if mode == "auto":
            path = urlsplit(target).path
            mode = (
                "x-api-key"
                if path == "/v1/messages"
                or path.startswith("/v1/messages/")
                or self.headers.get("anthropic-version")
                else "bearer"
            )
        if key:
            headers["x-api-key" if mode == "x-api-key" else "Authorization"] = (
                key if mode == "x-api-key" else f"Bearer {key}"
            )
        elif authorization:
            headers["Authorization"] = authorization
        elif api_key:
            headers["x-api-key"] = api_key
        return headers

    def _relay(self) -> None:
        route = self.server._acquire_request_route(
            self.headers.get_all(TUNNEL_REQUEST_HEADER, []),
            self.headers.get_all(TUNNEL_MODEL_HEADER, []),
        )
        if route is None:
            self._request_id = 0
            self._response_status = None
            self._response_bytes = 0
            self._error_detail = ""
            self._error(403, "Request rejected")
            return
        lease, tunnel_request, self._local_cancel_event = route
        spec = lease.spec
        upstream = spec.parsed_upstream
        started = time.monotonic()
        safe_path = "".join(
            character
            for character in urlsplit(self.path).path[:160]
            if character >= " " and character != "\x7f"
        )
        request_id = self.server.metrics.begin(
            spec.id, spec.name, self.command, safe_path
        )
        self._request_id = request_id
        self._response_status = None
        self._response_bytes = 0
        self._error_detail = ""
        request_bytes = 0
        cache_extended = failed = cancelled = False
        response_latency = None
        retries = retries_429 = 0
        queue_ms = 0.0
        connection = None
        buffered_body = None
        committed = False
        usage = TokenUsage()
        generation_started_at = started
        try:
            if tunnel_request and spec.auth_mode == "passthrough":
                self._error(503, "Tunnel route is unavailable")
                return
            ok, body = self._body()
            request_bytes = len(body or b"")
            self.server.metrics.add_bytes(bytes_in=request_bytes)
            if not ok:
                cancelled = self._response_status is None
                return
            request_content_type = self.headers.get("Content-Type", "")
            request_media_type = media_type(request_content_type)
            is_json = bool(body) and (
                request_media_type == "application/json"
                or request_media_type.endswith("+json")
            ) and not self.headers.get("Content-Encoding")
            payload = json_object(body) if is_json else None
            if (
                payload is None
                and body
                and not self.headers.get("Content-Encoding")
                and request_media_type == "multipart/form-data"
            ):
                payload = multipart_routing_payload(
                    request_content_type, body
                )
            request_target = self.path
            image_bridge = False
            routed_model = payload.get("model") if isinstance(payload, dict) else None
            if is_json:
                try:
                    prepared_image = prepare_image_request(
                        self.command, self.path, payload
                    )
                except InvalidImageRequest as error:
                    self._error(400, "Invalid image generation request", str(error))
                    return
                if prepared_image is not None:
                    request_target, body, payload = prepared_image
                    image_bridge = True
            model = request_model(payload)
            if tunnel_request:
                route_token = self.headers.get(TUNNEL_MODEL_HEADER, "")
                if (
                    not isinstance(routed_model, str)
                    or not secrets.compare_digest(
                        model_route_token(routed_model).encode(), route_token.encode()
                    )
                ):
                    self._error(403, "Request rejected")
                    return
            elif isinstance(routed_model, str):
                routed_lease = self.server.acquire_local_model_route(
                    spec.id, routed_model
                )
                if routed_lease is not None:
                    lease.release()
                    lease = routed_lease
                    spec = lease.spec
                    upstream = spec.parsed_upstream
                    self.server.metrics.route(request_id, spec.id, spec.name)
            stream = bool(payload and payload.get("stream") is True)
            uses_images = request_uses_images(payload, self.path)
            if spec.cache_1h and payload is not None:
                rewritten = echo_cache_for_one_hour(body, payload)
                cache_extended = rewritten is not body
                body = rewritten
            self.server.metrics.request(
                request_id, model, request_bytes, cache_extended
            )
            target = upstream_target(upstream, request_target)
            required_terminal = (
                "response.completed"
                if stream
                and self.command == "POST"
                and urlsplit(self.path).path.rstrip("/") == "/v1/responses"
                else ""
            )
            fallback_body = None
            if required_terminal and is_json:
                try:
                    fallback_payload = _load_json(body)
                    if not isinstance(fallback_payload, dict):
                        raise InvalidJson("Invalid request")
                    fallback_payload["stream"] = False
                    candidate = json.dumps(
                        fallback_payload,
                        separators=(",", ":"),
                        allow_nan=False,
                    ).encode("utf-8")
                    if len(candidate) <= MAX_REQUEST_BYTES:
                        fallback_body = candidate
                except (TypeError, ValueError):
                    pass
                finally:
                    fallback_payload = None
            payload = None
            if required_terminal:
                with self.server._config_lock:
                    if self._client_disconnected():
                        raise ClientDisconnected
                    self.server.register_response_client(
                        self.connection, self._local_cancel_event
                    )
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream; charset=utf-8")
                self.send_header("Cache-Control", "no-store")
                self.send_header("Connection", "close")
                self._finish_client_headers()
                self.close_connection = True
                self._response_status = 200
                self._sse_heartbeat_at = (
                    time.monotonic() + STREAM_HEARTBEAT_INTERVAL
                )
                committed = True
            fallback_active = False
            response_rewritten = False
            rewritten_content_type = ""
            response_content_type = ""
            response_expected_length = None
            response_stream = stream
            buffered_length = 0
            while True:
                attempt, waited = lease.runtime.acquire_attempt(
                    self._client_disconnected, model
                )
                if retries == 0:
                    queue_ms += waited
                self.server.metrics.dispatch(request_id, spec.id, queue_ms)
                response = None
                retry_status = None
                retry_header = None
                block_model = False
                balance_error = False
                activate_fallback = False
                try:
                    connection, attempt_target, proxy_headers = upstream_connection(
                        upstream,
                        target,
                        attempt.proxy_url,
                        timeout=(
                            STREAM_HEADER_TIMEOUT
                            if stream and not fallback_active
                            else STREAM_IDLE_TIMEOUT
                            if required_terminal
                            else UPSTREAM_TIMEOUT
                        ),
                    )
                    self.server.register_upstream(
                        connection, self.connection, self._local_cancel_event
                    )
                    install_cancelable_connect(
                        connection,
                        self.server.register_upstream_socket,
                        self._check_cancelled,
                    )
                    headers = self._request_headers(spec, target, attempt.api_key)
                    if required_terminal or image_bridge:
                        headers = {
                            name: value
                            for name, value in headers.items()
                            if name.casefold() != "accept-encoding"
                        }
                        headers["Accept-Encoding"] = "identity"
                    if fallback_active or image_bridge:
                        headers = {
                            name: value
                            for name, value in headers.items()
                            if name.casefold() not in REWRITTEN_REQUEST_HEADERS
                        }
                        headers["Accept"] = (
                            "text/event-stream" if image_bridge else "application/json"
                        )
                        headers["Content-Type"] = "application/json; charset=utf-8"
                    headers.update(proxy_headers)
                    connection.request(
                        self.command,
                        attempt_target,
                        body=body,
                        headers=headers,
                    )
                    self._check_cancelled()
                    upstream_sock = connection.sock
                    self.server.register_upstream_socket(connection, upstream_sock)
                    generation_started_at = time.monotonic()
                    response = connection.getresponse()
                    self._check_cancelled()
                    response_content_type = response.getheader("Content-Type") or ""
                    response_stream = (
                        stream and not fallback_active
                    ) or "text/event-stream" in response_content_type.casefold()
                    response_expected_length = (
                        response.length
                        if self.command != "HEAD" and response.status not in {204, 304}
                        else None
                    )
                    if upstream_sock is not None:
                        upstream_sock.settimeout(
                            STREAM_IDLE_TIMEOUT
                            if response_stream or required_terminal
                            else UPSTREAM_TIMEOUT
                        )
                    if response.status < 400:
                        attempt_terminal = required_terminal if response_stream else ""
                        if image_bridge:
                            encoding = response.getheader("Content-Encoding")
                            if encoding and encoding.strip().casefold() != "identity":
                                raise InvalidImageResponse(
                                    "Image generation returned an encoded response"
                                )
                        if image_bridge and response.status in {204, 304}:
                            raise InvalidImageResponse(
                                "Image generation omitted the response body"
                            )
                        if required_terminal and response.status in {204, 304}:
                            raise IncompleteSSE("Provider omitted the response body")
                        if attempt_terminal and media_type(response_content_type) != (
                            "text/event-stream"
                        ):
                            raise IncompleteSSE("Provider returned a non-SSE response")
                        if (
                            (image_bridge or not response_stream or attempt_terminal)
                            and self.command != "HEAD"
                            and response.status not in {204, 304}
                        ):
                            try:
                                buffered_body, buffered_length = buffered_response(
                                    response,
                                    response_expected_length,
                                    "" if image_bridge else attempt_terminal,
                                    (
                                        MAX_IMAGE_RESPONSE_BYTES
                                        if image_bridge
                                        else MAX_BUFFERED_RESPONSE_BYTES
                                    ),
                                    self._check_cancelled,
                                )
                            except (OSError, http.client.HTTPException) as error:
                                if image_bridge:
                                    raise InvalidImageResponse(
                                        "Image generation response is incomplete or too large"
                                    ) from error
                                raise
                            if image_bridge:
                                adapted = images_response(buffered_body.read())
                                buffered_body.close()
                                buffered_body = tempfile.SpooledTemporaryFile(
                                    max_size=RESPONSE_SPOOL_BYTES, mode="w+b"
                                )
                                buffered_body.write(adapted)
                                buffered_body.seek(0)
                                buffered_length = len(adapted)
                                response_content_type = "application/json"
                                response_rewritten = True
                                rewritten_content_type = "application/json"
                            if fallback_active and not response_stream:
                                buffered_body, buffered_length = completed_sse_response(
                                    buffered_body
                                )
                                response_content_type = "text/event-stream"
                                response_rewritten = True
                                rewritten_content_type = (
                                    "text/event-stream; charset=utf-8"
                                )
                        break
                    retry_status = response.status
                    retry_header = response.getheader("Retry-After")
                    if 400 <= retry_status < 500:
                        error_body = error_response_body(response, connection)
                        balance_error = balance_exhausted(
                            retry_status, error_body
                        )
                    if retry_status == 404:
                        block_model = model_unavailable_on_plan(
                            retry_status, error_body, model
                        )
                    retry_detail = (
                        "Key balance exhausted; paused until 00:00 MSK"
                        if balance_error
                        else http_error_detail(response.status)
                    )
                    if (
                        uses_images
                        and 400 <= retry_status < 500
                        and retry_status not in {401, 403, 408, 429}
                        and not balance_error
                        and not block_model
                    ):
                        failed = True
                        self._response_status = retry_status
                        response_latency = (time.monotonic() - started) * 1000
                        self.server.metrics.response(
                            request_id,
                            spec.id,
                            retry_status,
                            response_latency,
                            cache_extended,
                            generation_started_at,
                        )
                        self._error(retry_status, retry_detail)
                        return
                except (OSError, http.client.HTTPException) as error:
                    retry_detail = network_error_detail(error)
                    activate_fallback = (
                        fallback_body is not None
                        and not fallback_active
                        and isinstance(error, IncompleteSSE)
                    )
                    if activate_fallback:
                        body = fallback_body
                        fallback_active = True

                if self.server.stopping.is_set():
                    raise RelayStopping
                if self._client_disconnected():
                    raise ClientDisconnected

                retries += 1
                retries_429 += int(retry_status == 429)
                delay = 0 if activate_fallback else retry_after_seconds(
                    retry_header, retries
                )
                if response is not None:
                    response.close()
                if connection is not None:
                    self.server.unregister_upstream(connection)
                    connection.close()
                    connection = None
                shared_retry = (
                    balance_error or block_model or key_wide_failure(retry_status)
                )
                if shared_retry:
                    balance_deferred = balance_error and lease.runtime.defer_balance(
                        attempt, rate_limited=retry_status == 429
                    )
                    if not balance_deferred:
                        lease.runtime.defer_attempt(
                            attempt,
                            delay,
                            rate_limited=retry_status == 429,
                            block_model=block_model,
                            model=model,
                        )
                self.server.metrics.retry(
                    request_id,
                    spec.id,
                    (time.monotonic() - started) * 1000,
                    retries,
                    retries_429,
                    cache_extended,
                    delay,
                    status=retry_status,
                    detail=retry_detail,
                )
                if not shared_retry:
                    self._wait_retry(delay)
            if required_terminal:
                self._sse_heartbeat_at = None
            if self._client_disconnected():
                raise ClientDisconnected
            self._response_status = response.status
            response_latency = (time.monotonic() - started) * 1000
            expected_length = (
                buffered_length
                if buffered_body is not None
                else response_expected_length
            )
            self.server.metrics.response(
                request_id,
                spec.id,
                response.status,
                response_latency,
                cache_extended,
                generation_started_at,
            )
            if not committed:
                blocked = HOP_HEADERS | {
                    name.strip().lower()
                    for name in (response.getheader("Connection") or "").split(",")
                    if name.strip()
                }
                with self.server._config_lock:
                    if self._client_disconnected():
                        raise ClientDisconnected
                    self.server.register_response_client(
                        self.connection, self._local_cancel_event
                    )
                self.send_response(response.status)
                for name, value in response.getheaders():
                    lowered = name.lower()
                    if (
                        lowered not in blocked
                        and lowered != "content-length"
                        and not (
                            response_rewritten and lowered in REWRITTEN_RESPONSE_HEADERS
                        )
                    ):
                        self.send_header(name, value)
                if response_rewritten:
                    self.send_header("Content-Type", rewritten_content_type)
                if expected_length is not None:
                    self.send_header("Content-Length", str(expected_length))
                elif self.command == "HEAD":
                    declared_length = response.getheader("Content-Length")
                    if declared_length and declared_length.isdecimal():
                        self.send_header("Content-Length", declared_length)
                self.send_header("Connection", "close")
                self._finish_client_headers()
                self.close_connection = True
                committed = True
            else:
                expected_length = None
            inspector = ResponseInspector(response_content_type)
            if self.command != "HEAD" and response.status not in {204, 304}:
                source = buffered_body if buffered_body is not None else response
                read_chunk = source.read if buffered_body is not None else source.read1
                while chunk := read_chunk(64 * 1024):
                    if self._client_disconnected():
                        raise ClientDisconnected
                    inspector.feed(chunk)
                    usage = inspector.usage
                    self.server.metrics.tokens(
                        request_id, usage, inspector.terminal_at
                    )
                    self._write_client(chunk)
                if expected_length is not None and self._response_bytes != expected_length:
                    failed = True
                    self._error_detail = "Provider closed the response early"
            usage = inspector.finish()
            self.server.metrics.tokens(request_id, usage, inspector.terminal_at)
            if response.status >= 400 and not self._error_detail:
                self._error_detail = http_error_detail(response.status)
        except InvalidImageResponse as error:
            failed = True
            self._error_detail = str(error)
            self.server.metrics.set_detail(request_id, self._error_detail)
            if not committed:
                try:
                    self._error(502, "Image generation failed", self._error_detail)
                except ClientDisconnected:
                    cancelled = True
                    self.close_connection = True
        except (ClientDisconnected, RelayStopping):
            cancelled = True
            self.close_connection = True
            switched = (
                self._local_cancel_event is not None
                and self._local_cancel_event.is_set()
            )
            self._error_detail = (
                "Provider switched"
                if switched
                else "Relay stopped"
                if self.server.stopping.is_set()
                else "Client disconnected"
            )
            if switched and not committed:
                try:
                    self._error(503, "Request cancelled", self._error_detail)
                except ClientDisconnected:
                    pass
        except (OSError, http.client.HTTPException) as error:
            switched = (
                self._local_cancel_event is not None
                and self._local_cancel_event.is_set()
            )
            if switched or self.server.stopping.is_set() or self._client_disconnected():
                switched = (
                    self._local_cancel_event is not None
                    and self._local_cancel_event.is_set()
                )
                cancelled = True
                self.close_connection = True
                self._error_detail = (
                    "Provider switched"
                    if switched
                    else "Relay stopped"
                    if self.server.stopping.is_set()
                    else "Client disconnected"
                )
                if switched and not committed:
                    try:
                        self._error(503, "Request cancelled", self._error_detail)
                    except ClientDisconnected:
                        pass
            else:
                failed = True
                self._error_detail = network_error_detail(error)
                self.server.metrics.set_detail(request_id, self._error_detail)
                if not committed:
                    status = 504 if isinstance(error, (TimeoutError, socket.timeout)) else 502
                    try:
                        self._error(status, f"{spec.name} is unavailable", self._error_detail)
                    except ClientDisconnected:
                        cancelled = True
                        self.close_connection = True
        finally:
            try:
                if buffered_body is not None:
                    buffered_body.close()
                if connection is not None:
                    self.server.unregister_upstream(connection)
                    connection.close()
                self.server.unregister_response_client(
                    self.connection, self._local_cancel_event
                )
            finally:
                elapsed = (
                    response_latency
                    if response_latency is not None
                    else (time.monotonic() - started) * 1000
                )
                try:
                    event = self.server.metrics.finish(
                        request_id,
                        self._response_status,
                        elapsed,
                        self._response_bytes,
                        usage,
                        retries=retries,
                        retries_429=retries_429,
                        queue_ms=queue_ms,
                        failed=failed,
                        cancelled=cancelled,
                        error_detail=self._error_detail,
                        response_observed=response_latency is not None,
                    )
                    self.server.record(event)
                finally:
                    lease.release()

    do_DELETE = do_GET = do_HEAD = do_OPTIONS = do_PATCH = do_POST = do_PUT = _relay

    def _error(self, status: int, message: str, detail: str | None = None) -> None:
        body = json.dumps({"error": message}).encode("utf-8")
        self._response_status = status
        self._error_detail = detail or message
        if hasattr(self, "_request_id"):
            self.server.metrics.set_detail(self._request_id, self._error_detail)
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self._finish_client_headers()
        self.close_connection = True
        if self.command != "HEAD":
            self._write_client(body)

    def log_message(self, _format, *_args) -> None:
        pass


class RelayServer(ThreadingHTTPServer):
    allow_reuse_address = os.name != "nt"
    request_queue_size = 128
    daemon_threads = True

    def server_bind(self) -> None:
        if os.name == "nt":
            self.socket.setsockopt(
                socket.SOL_SOCKET,
                socket.SO_EXCLUSIVEADDRUSE,
                1,
            )
        super().server_bind()

    def __init__(
        self,
        *args,
        registry: ProviderRegistry | None = None,
        echo_rpm: int | None = None,
        echo_api_key: str | None = None,
        config: bool = True,
        config_path=None,
        history=True,
        **kwargs,
    ):
        self.metrics = RelayMetrics()
        self.stopping = threading.Event()
        self._upstream_lock = threading.Lock()
        self._upstreams = {}
        self._response_client_lock = threading.Lock()
        self._response_clients = {}
        self._local_cancel_event = threading.Event()
        self._client_monitor = None
        self._config_lock = threading.RLock()
        self._tunnel_route_marker = secrets.token_urlsafe(32)
        self._tunnel_model_catalog: tuple[str, ...] = ()
        self._tunnel_model_probes: dict[str, str] = {}
        self._tunnel_model_generation = 0
        self._tunnel_model_probe_slots = threading.BoundedSemaphore(
            MAX_TUNNEL_MODEL_PROBES
        )
        self._shared_tunnel_control = SharedTunnelControl()
        self._config_store = ConfigStore(config_path) if config and registry is None else None
        self.config_error = ""
        self.history_error = ""
        self.tunnel_history_error = ""
        key = environment_api_key() if echo_api_key is None else echo_api_key
        self._environment_key = normalize_api_key(key)
        saved = None
        if registry is not None:
            self.registry = registry
        elif self._config_store is not None:
            saved = self._config_store.load()
            self.registry = (
                ProviderRegistry.from_config(saved, key)
                if saved is not None
                else ProviderRegistry.defaults(key, echo_rpm or 0)
            )
            if saved is not None and echo_rpm is not None:
                self.registry.update("echo", rpm=echo_rpm)
        else:
            self.registry = ProviderRegistry.defaults(key, echo_rpm or 0)
        try:
            self._relay_model_routes = _tunnel_routes(
                saved.get("model_routes", {}) if saved else {},
                _tunnel_models(saved.get("model_route_order", ())) if saved else (),
                self.registry.active().id,
            )
            (
                self._tunnel_allowed_models,
                self._tunnel_model_routes,
                saved_tunnel_token,
                self._tunnel_rpm_per_ip,
                self._tunnel_context_limit_kib,
                self._tunnel_publisher_profile,
                self._tunnel_provider_id,
            ) = _tunnel_settings(saved, self.registry.active().id)
            provider_ids = {provider.id for provider in self.registry.list()}
            if (
                self._tunnel_provider_id not in provider_ids
                or not set(self._tunnel_model_routes.values()) <= provider_ids
                or not set(self._relay_model_routes.values()) <= provider_ids
            ):
                raise ValueError("Saved tunnel settings are invalid")
        except BaseException:
            self.registry.close()
            raise
        self._tunnel_token = saved_tunnel_token or secrets.token_urlsafe(32)
        self.history = None
        self.tunnel_history = None
        self.tunnel = None
        super().__init__(*args, **kwargs)
        try:
            if history is True:
                try:
                    self.history = HistoryStore()
                except (OSError, sqlite3.Error):
                    # History is useful telemetry, never a reason for the relay to fail.
                    self.history_error = "History unavailable"
                try:
                    self.tunnel_history = TunnelHistoryStore()
                except (OSError, sqlite3.Error):
                    self.tunnel_history_error = "Tunnel history unavailable"
            else:
                self.history = history or None
            self.tunnel = TunnelController(
                (LISTEN[0], self.server_port),
                self._tunnel_token,
                self._tunnel_allowed_models,
                sensitive_markers=self._tunnel_sensitive_markers(),
                secret_markers=self._tunnel_secret_markers(),
                route_guard=self._tunnel_route_safe,
                policy_lock=self._config_lock,
                public_rpm=self._tunnel_rpm_per_ip,
                context_limit_kib=self._tunnel_context_limit_kib,
                history_loader=(
                    lambda: self.tunnel_history.recent(256)
                    if self.tunnel_history is not None
                    else ()
                ),
                event_sink=self.record_tunnel_event,
                publisher_profile=self._tunnel_publisher_profile,
                route_marker=self._tunnel_route_marker,
            )
            if saved is not None and self._config_store is not None:
                self._config_store.save(self._config_value())
            recent = getattr(self.history, "recent", None)
            if recent is not None:
                try:
                    self.metrics.restore_recent(recent("all", 100))
                except (KeyError, OSError, TypeError, ValueError, sqlite3.Error):
                    self.history_error = "History read failed"
            self._client_monitor = threading.Thread(
                target=self._monitor_client_disconnects,
                name="relay-client-monitor",
                daemon=True,
            )
            self._client_monitor.start()
        except BaseException:
            if self.tunnel is not None:
                self.tunnel.stop()
            if self.history:
                self.history.close()
            if self.tunnel_history:
                self.tunnel_history.close()
            self.registry.close()
            super().server_close()
            raise

    def _config_value(self) -> dict:
        value = self.registry.export_config(self._environment_key)
        value["model_routes"] = dict(self._relay_model_routes)
        value["model_route_order"] = list(self._relay_model_routes)
        value["tunnel"] = {
            "allowed_models": list(self._tunnel_allowed_models),
            "model_routes": dict(self._tunnel_model_routes),
            "access_token": self._tunnel_token,
            "rpm_per_ip": self._tunnel_rpm_per_ip,
            "context_limit_kib": self._tunnel_context_limit_kib,
            "publisher_profile": self._tunnel_publisher_profile,
            "provider_id": self._tunnel_provider_id,
        }
        return value

    def _tunnel_sensitive_markers(self) -> tuple[str, ...]:
        markers = {
            self._tunnel_token,
            self._tunnel_route_marker,
            self._environment_key,
        }
        for provider in self.registry.export_config().get("providers", ()):
            if provider.get("auth_mode") == "passthrough":
                continue
            markers.update(
                (
                    provider.get("id", ""),
                    provider.get("name", ""),
                    provider.get("upstream", ""),
                )
            )
            upstream = urlsplit(provider.get("upstream", ""))
            upstream_host = upstream.hostname or ""
            markers.update(_hostname_privacy_markers(upstream_host))
            for key in provider.get("keys", ()):
                markers.update((key.get("key", ""), key.get("proxy", "")))
                proxy_value = key.get("proxy", "")
                proxy = urlsplit(
                    proxy_value
                    if "://" in proxy_value
                    else "http://" + proxy_value
                )
                markers.update(_hostname_privacy_markers(proxy.hostname or ""))
                markers.update(
                    (unquote(proxy.username or ""), unquote(proxy.password or ""))
                )
        return tuple(
            marker.strip()
            for marker in markers
            if isinstance(marker, str) and marker.strip()
        )

    def _tunnel_secret_markers(self) -> tuple[str, ...]:
        markers = {
            self._tunnel_token,
            self._tunnel_route_marker,
            self._environment_key,
        }
        for provider in self.registry.export_config().get("providers", ()):
            if provider.get("auth_mode") == "passthrough":
                continue
            for key in provider.get("keys", ()):
                markers.add(key.get("key", ""))
                proxy_value = key.get("proxy", "")
                proxy = urlsplit(
                    proxy_value
                    if "://" in proxy_value
                    else "http://" + proxy_value
                )
                markers.update(
                    (unquote(proxy.username or ""), unquote(proxy.password or ""))
                )
        return tuple(
            marker.strip()
            for marker in markers
            if isinstance(marker, str) and marker.strip()
        )

    def _tunnel_route_safe(self) -> bool:
        try:
            providers = {provider.id: provider for provider in self.registry.list()}
            routed = set(self._tunnel_model_routes.values())
            return bool(routed) and not self.stopping.is_set() and all(
                provider_id in providers
                and providers[provider_id].auth_mode != "passthrough"
                for provider_id in routed
            )
        except (KeyError, RuntimeError):
            return False

    def _invalidate_tunnel_model_probes_locked(self) -> None:
        self._tunnel_model_generation += 1
        self._tunnel_model_probes.clear()

    def _clear_tunnel_model_probes_locked(self) -> None:
        self._tunnel_model_catalog = ()
        self._invalidate_tunnel_model_probes_locked()

    def _sync_tunnel_security(self) -> None:
        if self.tunnel is not None:
            self.tunnel.configure(
                token=self._tunnel_token,
                allowed_models=self._tunnel_allowed_models,
                sensitive_markers=self._tunnel_sensitive_markers(),
                secret_markers=self._tunnel_secret_markers(),
                public_rpm=self._tunnel_rpm_per_ip,
                context_limit_kib=self._tunnel_context_limit_kib,
                publisher_profile=self._tunnel_publisher_profile,
                route_marker=self._tunnel_route_marker,
            )

    def _acquire_request_route(self, marker_values, model_values):
        with self._config_lock:
            if not marker_values:
                return (
                    self.registry.acquire_active(),
                    False,
                    self._local_cancel_event,
                )
            if (
                len(marker_values) != 1
                or not isinstance(marker_values[0], str)
                or not marker_values[0].isascii()
                or not secrets.compare_digest(
                    marker_values[0].encode(), self._tunnel_route_marker.encode()
                )
            ):
                return None
            if (
                len(model_values) != 1
                or not isinstance(model_values[0], str)
                or len(model_values[0]) != 64
                or any(character not in "0123456789abcdef" for character in model_values[0])
            ):
                return None
            provider_id = next(
                (
                    provider_id
                    for model, provider_id in self._tunnel_model_routes.items()
                    if secrets.compare_digest(
                        model_route_token(model).encode(), model_values[0].encode()
                    )
                ),
                None,
            )
            return (
                (self.registry.acquire(provider_id), True, None)
                if provider_id is not None
                else None
            )

    def acquire_local_model_route(self, current_provider_id: str, model: str):
        with self._config_lock:
            provider_id = self._relay_model_routes.get(model)
            if provider_id is None or provider_id == current_provider_id:
                return None
            return self.registry.acquire(provider_id)

    def _persist(self) -> None:
        if self._config_store is None:
            return
        try:
            self._config_store.save(self._config_value())
        except ConfigError:
            self.config_error = "Settings changed for this session but were not saved"
        else:
            self.config_error = ""

    def register_upstream(self, connection, client=None, cancel_event=None) -> None:
        with self._upstream_lock:
            if self.stopping.is_set():
                connection.close()
                raise RelayStopping
            if cancel_event is not None and cancel_event.is_set():
                connection.close()
                raise ClientDisconnected
            self._upstreams[connection] = (client, None, cancel_event)

    def register_response_client(self, client, cancel_event) -> None:
        with self._response_client_lock:
            if self.stopping.is_set():
                raise RelayStopping
            if cancel_event is not None and cancel_event.is_set():
                raise ClientDisconnected
            self._response_clients[client] = cancel_event

    def unregister_response_client(self, client, cancel_event) -> None:
        with self._response_client_lock:
            if (
                client in self._response_clients
                and self._response_clients[client] is cancel_event
            ):
                self._response_clients.pop(client, None)

    def _abort_response_clients(self, cancel_event=None) -> None:
        with self._response_client_lock:
            clients = tuple(
                client
                for client, event in self._response_clients.items()
                if cancel_event is None or event is cancel_event
            )
            for client in clients:
                self._response_clients.pop(client, None)
        for client in clients:
            _abort_socket(client)

    def register_upstream_socket(self, connection, sock) -> None:
        with self._upstream_lock:
            current = self._upstreams.get(connection)
            if current is not None:
                self._upstreams[connection] = (current[0], sock, current[2])
                return
        _abort_upstream(connection, sock)
        if self.stopping.is_set():
            raise RelayStopping
        raise ClientDisconnected

    def unregister_upstream(self, connection) -> None:
        with self._upstream_lock:
            self._upstreams.pop(connection, None)

    def _monitor_client_disconnects(self) -> None:
        while not self.stopping.is_set():
            with self._upstream_lock:
                watched = {
                    client: connection
                    for connection, (client, _sock, _cancel) in self._upstreams.items()
                    if client is not None
                }
            if not watched:
                self.stopping.wait(0.1)
                continue
            clients = tuple(watched)
            readable = []
            for offset in range(0, len(clients), SELECT_SOCKET_LIMIT):
                try:
                    ready, _, _ = select.select(
                        clients[offset : offset + SELECT_SOCKET_LIMIT], [], [], 0
                    )
                except (OSError, ValueError):
                    continue
                readable.extend(ready)
            for client in readable:
                try:
                    disconnected = client.recv(1, socket.MSG_PEEK) == b""
                except (OSError, ValueError):
                    disconnected = True
                if not disconnected:
                    continue
                connection = watched[client]
                with self._upstream_lock:
                    current = self._upstreams.get(connection)
                    if current is None or current[0] is not client:
                        continue
                    self._upstreams.pop(connection, None)
                _abort_upstream(connection, current[1])
            self.stopping.wait(0.1)

    def _abort_upstreams(self, cancel_event=None) -> None:
        with self._upstream_lock:
            connections = tuple(
                (connection, state)
                for connection, state in self._upstreams.items()
                if cancel_event is None or state[2] is cancel_event
            )
            for connection, _state in connections:
                self._upstreams.pop(connection, None)
        for connection, (_client, sock, _cancel) in connections:
            _abort_upstream(connection, sock)

    def _rotate_local_requests_locked(self):
        cancel_event = self._local_cancel_event
        cancel_event.set()
        self._local_cancel_event = threading.Event()
        return cancel_event

    def _abort_local_requests(self, cancel_event) -> None:
        self._abort_response_clients(cancel_event)
        self._abort_upstreams(cancel_event)

    def _finish_local_route_change(self, cancel_event) -> None:
        if cancel_event is None:
            return
        try:
            self._abort_local_requests(cancel_event)
        finally:
            with self._config_lock:
                self._persist()

    def providers(self):
        rates = self.metrics.provider_actual_rpm()
        return tuple(
            replace(provider, actual_rpm=rates.get(provider.id, 0))
            for provider in self.registry.list()
        )

    def provider(self):
        active = self.registry.active()
        return active.name, urlsplit(active.upstream)

    def select(self, provider):
        cancel_event = None
        with self._config_lock:
            previous_id = self.registry.active().id
            selected = self.registry.select(provider)
            if selected.id != previous_id:
                cancel_event = self._rotate_local_requests_locked()
            else:
                self._persist()
        self._finish_local_route_change(cancel_event)
        return selected.name, urlsplit(selected.upstream)

    def toggle(self):
        cancel_event = None
        with self._config_lock:
            previous_id = self.registry.active().id
            selected = self.registry.toggle()
            if selected.id != previous_id:
                cancel_event = self._rotate_local_requests_locked()
            else:
                self._persist()
        self._finish_local_route_change(cancel_event)
        return selected.name, urlsplit(selected.upstream)

    def add_provider(self, **values):
        with self._config_lock:
            provider = self.registry.add(**values)
            self._sync_tunnel_security()
            self._persist()
            return provider

    def update_provider(self, provider_id, **values):
        stop_tunnel = False
        cancel_event = None
        with self._config_lock:
            active_id = self.registry.active().id
            previous = next(
                (
                    provider
                    for provider in self.registry.list()
                    if provider.id == provider_id
                ),
                None,
            )
            if previous is None:
                raise KeyError("Provider not found")
            provider = self.registry.update(provider_id, **values)
            if (
                provider_id == active_id
                or provider_id in self._relay_model_routes.values()
            ) and (
                previous.upstream != provider.upstream
                or previous.auth_mode != provider.auth_mode
                or "api_keys" in values
            ):
                cancel_event = self._rotate_local_requests_locked()
            route_changed = provider_id in self._tunnel_model_routes.values() and (
                previous.upstream != provider.upstream
                or previous.auth_mode != provider.auth_mode
            )
            if route_changed:
                self._tunnel_model_routes = {
                    model: routed_provider
                    for model, routed_provider in self._tunnel_model_routes.items()
                    if routed_provider != provider_id
                }
                self._tunnel_allowed_models = tuple(self._tunnel_model_routes)
                self._tunnel_route_marker = secrets.token_urlsafe(32)
                stop_tunnel = True
            if provider_id == self._tunnel_provider_id and (
                previous.upstream != provider.upstream
                or previous.auth_mode != provider.auth_mode
                or "api_keys" in values
            ):
                self._clear_tunnel_model_probes_locked()
            self._sync_tunnel_security()
            if cancel_event is None:
                self._persist()
        self._finish_local_route_change(cancel_event)
        if stop_tunnel:
            self.tunnel.stop()
        return provider

    def delete_provider(self, provider_id):
        cancel_event = None
        with self._config_lock:
            previous_active_id = self.registry.active().id
            catalog_provider = provider_id == self._tunnel_provider_id
            local_routed_provider = provider_id in self._relay_model_routes.values()
            routed_provider = provider_id in self._tunnel_model_routes.values()
            if (catalog_provider or routed_provider) and self.tunnel.snapshot()[
                "state"
            ] not in {"stopped", "error"}:
                raise RuntimeError("Stop the tunnel before deleting its provider")
            provider = self.registry.delete(provider_id)
            self._relay_model_routes = {
                model: routed
                for model, routed in self._relay_model_routes.items()
                if routed != provider_id
            }
            if provider.id != previous_active_id or local_routed_provider:
                cancel_event = self._rotate_local_requests_locked()
            if routed_provider:
                self._tunnel_model_routes = {
                    model: routed
                    for model, routed in self._tunnel_model_routes.items()
                    if routed != provider_id
                }
                self._tunnel_allowed_models = tuple(self._tunnel_model_routes)
                self._tunnel_route_marker = secrets.token_urlsafe(32)
            if catalog_provider:
                self._tunnel_provider_id = self.registry.active().id
                self._clear_tunnel_model_probes_locked()
            self._sync_tunnel_security()
            if cancel_event is None:
                self._persist()
        self._finish_local_route_change(cancel_event)
        return provider

    def add_provider_key(self, provider_id, api_key, rpm=0, proxy_url=""):
        with self._config_lock:
            fingerprint = self.registry.add_key(provider_id, api_key, rpm, proxy_url)
            if provider_id == self._tunnel_provider_id:
                self._invalidate_tunnel_model_probes_locked()
            self._sync_tunnel_security()
            self._persist()
            return fingerprint

    def remove_provider_key(self, provider_id, fingerprint):
        if (
            provider_id == "echo"
            and self._environment_key
            and fingerprint == key_fingerprint(self._environment_key)
        ):
            raise ValueError("FREEMODEL_API_KEY is managed by Windows environment")
        with self._config_lock:
            result = self.registry.remove_key(provider_id, fingerprint)
            if provider_id == self._tunnel_provider_id:
                self._invalidate_tunnel_model_probes_locked()
            self._sync_tunnel_security()
            self._persist()
            return result

    def update_provider_key(self, provider_id, fingerprint, rpm, proxy_url=None):
        environment_key = (
            provider_id == "echo"
            and self._environment_key
            and fingerprint == key_fingerprint(self._environment_key)
        )
        if environment_key and proxy_url not in {None, ""}:
            raise ValueError("FREEMODEL_API_KEY must use Direct")
        with self._config_lock:
            result = self.registry.update_key(
                provider_id, fingerprint, rpm, proxy_url
            )
            if provider_id == self._tunnel_provider_id:
                self._invalidate_tunnel_model_probes_locked()
            self._sync_tunnel_security()
            self._persist()
            return result

    def move_provider_key(self, provider_id, fingerprint, direction):
        if type(direction) is not int or direction not in (-1, 1):
            raise ValueError("Key direction must be -1 or 1")
        with self._config_lock:
            rows = self.registry.key_views(provider_id)
            index = next(
                (index for index, row in enumerate(rows) if row["id"] == fingerprint),
                None,
            )
            if index is None:
                raise KeyError("API key not found")
            environment_id = (
                key_fingerprint(self._environment_key)
                if provider_id == "echo" and self._environment_key
                else ""
            )
            if fingerprint == environment_id:
                raise ValueError("Env Lite must stay first")
            if environment_id and direction == -1 and index == 1:
                raise ValueError("Env Lite must stay first")
            result = self.registry.move_key(provider_id, fingerprint, direction)
            self._sync_tunnel_security()
            self._persist()
            return result

    def reset_provider_key_cooldown(self, provider_id, fingerprint):
        with self._config_lock:
            return self.registry.reset_key_cooldown(provider_id, fingerprint)

    def provider_keys(self, provider_id):
        rows = self.registry.key_views(provider_id)
        environment_id = (
            key_fingerprint(self._environment_key)
            if provider_id == "echo" and self._environment_key
            else ""
        )
        minimum = 1 if environment_id else 0
        return tuple(
            {
                **row,
                "label": (
                    f"Env Lite · {environment_id}"
                    if row["id"] == environment_id
                    else row["label"]
                ),
                "pinned": row["id"] == environment_id,
                "can_move_up": row["id"] != environment_id and index > minimum,
                "can_move_down": row["id"] != environment_id and index < len(rows) - 1,
            }
            for index, row in enumerate(rows)
        )

    def configure_echo(self, api_key=None, rpm=None):
        values = {}
        if api_key is not None:
            values["api_keys"] = (api_key,) if api_key else ()
        if rpm is not None:
            values["rpm"] = rpm
        with self._config_lock:
            provider = self.registry.update("echo", **values)
            self._sync_tunnel_security()
            self._persist()
            return provider

    def echo_settings(self):
        view = next(provider for provider in self.providers() if provider.id == "echo")
        return {
            "rpm": view.rpm,
            "queued": view.queued,
            "cooldown_ms": view.cooldown_ms,
            "echo_retries_429": view.retries_429,
            "key_configured": view.key_count > 0,
        }

    def tunnel_snapshot(self) -> dict:
        snapshot = self.tunnel.snapshot()
        snapshot["route_available"] = self._tunnel_route_safe()
        if "recent" not in snapshot:
            try:
                recent = (
                    tuple(self.tunnel_history.recent(256))
                    if self.tunnel_history is not None
                    else ()
                )
            except (OSError, TypeError, ValueError, sqlite3.Error):
                recent = ()
                self.tunnel_history_error = "Tunnel history read failed"
            snapshot.update(clients=(), live=(), recent=recent)
        snapshot["history_error"] = self.tunnel_history_error
        return snapshot

    def shared_tunnels(self) -> dict:
        return self._shared_tunnel_control.snapshot()

    def control_shared_tunnel(
        self, position: int, revision: int, action: str
    ) -> dict:
        return self._shared_tunnel_control.control(position, revision, action)

    def tunnel_allowed_models(self) -> tuple[str, ...]:
        with self._config_lock:
            return self._tunnel_allowed_models

    def relay_model_routes(self) -> tuple[dict[str, str], ...]:
        with self._config_lock:
            return tuple(
                {"model": model, "provider_id": provider_id}
                for model, provider_id in self._relay_model_routes.items()
            )

    def set_relay_allowed_models(self, models) -> tuple[str, ...]:
        selected = _tunnel_models(models)
        cancel_event = None
        with self._config_lock:
            provider_id = self._tunnel_provider_id
            if self._tunnel_model_catalog and not set(selected) <= set(
                self._tunnel_model_catalog
            ):
                raise ValueError("Relay model is not in the current catalog")
            routes = {
                model: routed_provider
                for model, routed_provider in self._relay_model_routes.items()
                if routed_provider != provider_id
            }
            routes.update((model, provider_id) for model in selected)
            if routes != self._relay_model_routes:
                self._relay_model_routes = routes
                cancel_event = self._rotate_local_requests_locked()
            else:
                self._persist()
            saved = tuple(
                model
                for model, routed_provider in self._relay_model_routes.items()
                if routed_provider == provider_id
            )
        self._finish_local_route_change(cancel_event)
        return saved

    def tunnel_model_routes(self) -> tuple[dict[str, str], ...]:
        with self._config_lock:
            return tuple(
                {"model": model, "provider_id": provider_id}
                for model, provider_id in self._tunnel_model_routes.items()
            )

    def tunnel_provider_id(self) -> str:
        with self._config_lock:
            return self._tunnel_provider_id

    def set_tunnel_provider(self, provider_id: str) -> str:
        with self._config_lock:
            if provider_id not in {
                provider.id for provider in self.registry.list()
            }:
                raise KeyError("Provider not found")
            if provider_id == self._tunnel_provider_id:
                return provider_id
            self._tunnel_provider_id = provider_id
            self._clear_tunnel_model_probes_locked()
            self._persist()
            return provider_id

    def fetch_tunnel_models(self):
        with self._config_lock:
            provider_id = self._tunnel_provider_id
            route_marker = self._tunnel_route_marker
            model_generation = self._tunnel_model_generation
        result = self.fetch_models(provider_id)
        catalog = _tunnel_catalog_models(result)
        with self._config_lock:
            if (
                provider_id != self._tunnel_provider_id
                or model_generation != self._tunnel_model_generation
                or not secrets.compare_digest(
                    route_marker.encode(), self._tunnel_route_marker.encode()
                )
            ):
                raise RuntimeError("Tunnel model catalog is stale")
            self._tunnel_model_catalog = catalog
            self._tunnel_model_probes = {
                model: state
                for model, state in self._tunnel_model_probes.items()
                if model in catalog and state in TUNNEL_MODEL_PROBE_STATES
            }
        return result

    def tunnel_model_probes(self) -> tuple[dict[str, str], ...]:
        with self._config_lock:
            return tuple(
                {"model": model, "state": self._tunnel_model_probes[model]}
                for model in self._tunnel_model_catalog
                if model in self._tunnel_model_probes
            )

    def probe_tunnel_model(self, model: str) -> dict[str, str]:
        model = _tunnel_models((model,))[0]
        with self._config_lock:
            if model not in self._tunnel_model_catalog:
                raise ValueError("Tunnel model is not in the current catalog")
            if self._tunnel_model_probes.get(model) == "testing":
                return {"model": model, "state": "testing"}
            provider_id = self._tunnel_provider_id
            route_marker = self._tunnel_route_marker
            model_generation = self._tunnel_model_generation
            if not self._tunnel_model_probe_slots.acquire(blocking=False):
                self._tunnel_model_probes[model] = "timeout"
                return {"model": model, "state": "timeout"}
            self._tunnel_model_probes[model] = "testing"

        deadline = time.monotonic() + TUNNEL_MODEL_PROBE_TIMEOUT
        cancelled = threading.Event()
        completed = threading.Event()
        holder_lock = threading.Lock()
        holder = [None]
        outcome = ["unavailable"]

        def run_probe() -> None:
            try:
                try:
                    outcome[0] = self._probe_tunnel_model_once(
                        provider_id,
                        model,
                        deadline,
                        cancelled,
                        holder_lock,
                        holder,
                    )
                except Exception:
                    outcome[0] = "unavailable"
            finally:
                self._tunnel_model_probe_slots.release()
                completed.set()

        worker = threading.Thread(
            target=run_probe,
            name="tunnel-model-probe",
            daemon=True,
        )
        try:
            worker.start()
        except BaseException:
            self._tunnel_model_probe_slots.release()
            state = "unavailable"
        else:
            completed.wait(max(0.0, deadline - time.monotonic()))
            if completed.is_set():
                state = outcome[0]
            else:
                cancelled.set()
                with holder_lock:
                    connection = holder[0]
                if connection is not None:
                    self.unregister_upstream(connection)
                    _abort_upstream(connection, getattr(connection, "sock", None))
                # Let the cancelled worker release its bounded probe slot before
                # the UI schedules the next model; otherwise one timeout can
                # cascade into immediate false timeouts for the remaining list.
                completed.wait(0.25)
                state = "timeout"

        with self._config_lock:
            current = (
                provider_id == self._tunnel_provider_id
                and model_generation == self._tunnel_model_generation
                and secrets.compare_digest(
                    route_marker.encode(), self._tunnel_route_marker.encode()
                )
                and model in self._tunnel_model_catalog
            )
            if current:
                self._tunnel_model_probes[model] = state
                return {"model": model, "state": state}
            return {"model": model, "state": "unavailable"}

    def _probe_tunnel_model_once(
        self,
        provider_id: str,
        model: str,
        deadline: float,
        cancelled: threading.Event,
        holder_lock: threading.Lock,
        holder: list,
    ) -> str:
        def remaining() -> float:
            value = deadline - time.monotonic()
            if value <= 0 or cancelled.is_set():
                raise TimeoutError
            return value

        lease = self.registry.acquire(provider_id)
        connection = response = None
        registered = False
        try:
            spec = lease.spec
            if spec.auth_mode == "passthrough":
                return "unavailable"
            attempt, _waited = lease.runtime.acquire_attempt(
                lambda: (
                    cancelled.is_set()
                    or self.stopping.is_set()
                    or time.monotonic() >= deadline
                ),
                model,
            )
            body = json.dumps(
                {
                    "model": model,
                    "messages": [{"role": "user", "content": "Reply OK"}],
                    "max_tokens": 1,
                    "stream": False,
                },
                separators=(",", ":"),
            ).encode("utf-8")
            upstream = spec.parsed_upstream
            target = upstream_target(upstream, "/v1/chat/completions")
            connection, request_target, proxy_headers = upstream_connection(
                upstream,
                target,
                attempt.proxy_url,
                timeout=remaining(),
            )
            self.register_upstream(connection)
            registered = True
            install_cancelable_connect(
                connection,
                self.register_upstream_socket,
                remaining,
            )
            with holder_lock:
                holder[0] = connection
            remaining()
            headers = {
                "Content-Type": "application/json",
                "Accept": "application/json",
                "Accept-Encoding": "identity",
                "Connection": "close",
                **proxy_headers,
            }
            if attempt.api_key:
                if spec.auth_mode == "x-api-key":
                    headers["x-api-key"] = attempt.api_key
                else:
                    headers["Authorization"] = f"Bearer {attempt.api_key}"
            connection.request("POST", request_target, body=body, headers=headers)
            upstream_socket = connection.sock
            self.register_upstream_socket(connection, upstream_socket)
            if upstream_socket is not None:
                upstream_socket.settimeout(remaining())
            response = connection.getresponse()
            if connection.sock is not None:
                connection.sock.settimeout(remaining())
            response_body = response.read(MAX_ERROR_BYTES + 1)
            if response.status >= 400:
                balance_error = balance_exhausted(response.status, response_body)
                block_model = model_unavailable_on_plan(
                    response.status, response_body, model
                )
                if balance_error or block_model:
                    balance_deferred = balance_error and lease.runtime.defer_balance(
                        attempt, rate_limited=False
                    )
                    if not balance_deferred:
                        lease.runtime.defer_attempt(
                            attempt,
                            0,
                            rate_limited=False,
                            block_model=block_model,
                            model=model,
                        )
                    with holder_lock:
                        if holder[0] is connection:
                            holder[0] = None
                    response.close()
                    response = None
                    if registered:
                        self.unregister_upstream(connection)
                        registered = False
                    connection.close()
                    connection = None
                    return self._probe_tunnel_model_once(
                        provider_id,
                        model,
                        deadline,
                        cancelled,
                        holder_lock,
                        holder,
                    )
            payload = (
                json_object(response_body)
                if response.status == 200 and len(response_body) <= MAX_ERROR_BYTES
                else None
            )
            choices = payload.get("choices") if payload is not None else None
            choice = choices[0] if isinstance(choices, list) and choices else None
            valid_completion = isinstance(choice, dict) and (
                isinstance(choice.get("message"), dict)
                or isinstance(choice.get("text"), str)
            )
            return "available" if valid_completion else "unavailable"
        except (socket.timeout, TimeoutError):
            return "timeout"
        except (ClientDisconnected, RelayStopping, OSError, http.client.HTTPException):
            return (
                "timeout"
                if cancelled.is_set() or time.monotonic() >= deadline
                else "unavailable"
            )
        finally:
            with holder_lock:
                if holder[0] is connection:
                    holder[0] = None
            if response is not None:
                response.close()
            if connection is not None:
                if registered:
                    self.unregister_upstream(connection)
                connection.close()
            lease.release()

    def tunnel_rpm_per_ip(self) -> int:
        with self._config_lock:
            return self._tunnel_rpm_per_ip

    def set_tunnel_rpm_per_ip(self, rpm: int) -> int:
        if not isinstance(rpm, int) or isinstance(rpm, bool) or rpm < 0:
            raise ValueError("Tunnel RPM must be a non-negative integer")
        with self._config_lock:
            self.tunnel.configure(public_rpm=rpm)
            self._tunnel_rpm_per_ip = rpm
            self._persist()
            return rpm

    def tunnel_context_limit_kib(self) -> int:
        with self._config_lock:
            return self._tunnel_context_limit_kib

    def set_tunnel_context_limit_kib(self, limit: int) -> int:
        limit = validate_context_limit_kib(limit)
        with self._config_lock:
            self.tunnel.configure(context_limit_kib=limit)
            self._tunnel_context_limit_kib = limit
            self._persist()
            return limit

    def tunnel_publisher_profile(self) -> str:
        with self._config_lock:
            return self._tunnel_publisher_profile

    def set_tunnel_publisher_profile(self, profile: str) -> str:
        parse_publisher_profile(profile)
        with self._config_lock:
            self.tunnel.configure(publisher_profile=profile)
            self._tunnel_publisher_profile = profile
            self._persist()
            return profile

    def set_tunnel_allowed_models(self, models) -> tuple[str, ...]:
        selected = _tunnel_models(models)
        with self._config_lock:
            provider_id = self._tunnel_provider_id
            if self._tunnel_model_catalog and not set(selected) <= set(
                self._tunnel_model_catalog
            ):
                raise ValueError("Tunnel model is not in the current catalog")
            self._tunnel_model_routes = {
                model: routed_provider
                for model, routed_provider in self._tunnel_model_routes.items()
                if routed_provider != provider_id
            }
            self._tunnel_model_routes.update(
                (model, provider_id) for model in selected
            )
            self._tunnel_allowed_models = tuple(self._tunnel_model_routes)
            self._sync_tunnel_security()
            self._persist()
            return tuple(
                model
                for model, routed_provider in self._tunnel_model_routes.items()
                if routed_provider == provider_id
            )

    def tunnel_access_token(self) -> str:
        with self._config_lock:
            self._persist()
            return self._tunnel_token

    def rotate_tunnel_token(self) -> str:
        with self._config_lock:
            self._tunnel_token = secrets.token_urlsafe(32)
            self._sync_tunnel_security()
            self._persist()
            return self._tunnel_token

    def start_tunnel(self) -> dict:
        with self._config_lock:
            if not self._tunnel_allowed_models:
                raise ValueError("Select at least one tunnel model")
            if not self._tunnel_route_safe():
                raise ValueError("The selected provider cannot be shared through a tunnel")
            self._sync_tunnel_security()
            self._persist()
        try:
            shared = self._shared_tunnel_control.ensure_self_running()
        except RuntimeError as error:
            raise ValueError("Shared tunnel state could not be resumed") from error
        if shared.get("available") is True and not any(
            tunnel.get("name") == CONTROL_SELF_NAME
            and tunnel.get("state") == "running"
            for tunnel in shared.get("tunnels", ())
        ):
            raise ValueError("Shared tunnel state could not be resumed")
        self.tunnel.start()
        return self.tunnel_snapshot()

    def stop_tunnel(self) -> dict:
        self.tunnel.stop()
        return self.tunnel_snapshot()

    def fetch_models(self, provider_id):
        with self._config_lock:
            cancel_event = self._local_cancel_event

        def check_cancelled():
            if self.stopping.is_set():
                raise RelayStopping
            if cancel_event.is_set():
                raise ClientDisconnected

        lease = self.registry.acquire(provider_id)
        try:
            spec = lease.spec
            upstream = spec.parsed_upstream
            target = upstream_target(upstream, "/v1/models")
            retries = 0
            while True:
                attempt, _waited = lease.runtime.acquire_attempt(
                    lambda: self.stopping.is_set() or cancel_event.is_set(), ""
                )
                connection = response = None
                retry_status = None
                retry_header = None
                try:
                    connection, request_target, proxy_headers = upstream_connection(
                        upstream, target, attempt.proxy_url, timeout=15
                    )
                    self.register_upstream(
                        connection, cancel_event=cancel_event
                    )
                    install_cancelable_connect(
                        connection,
                        self.register_upstream_socket,
                        check_cancelled,
                    )
                    headers = {}
                    if attempt.api_key and spec.auth_mode != "passthrough":
                        name = (
                            "x-api-key"
                            if spec.auth_mode == "x-api-key"
                            else "Authorization"
                        )
                        headers[name] = (
                            attempt.api_key
                            if name == "x-api-key"
                            else f"Bearer {attempt.api_key}"
                        )
                    headers.update(proxy_headers)
                    connection.request("GET", request_target, headers=headers)
                    self.register_upstream_socket(connection, connection.sock)
                    response = connection.getresponse()
                    if response.status < 400:
                        body = response.read(MAX_INSPECT_BYTES + 1)
                        if len(body) > MAX_INSPECT_BYTES:
                            raise ValueError("Model catalog is too large")
                        payload = json.loads(body)
                        if not isinstance(payload, (dict, list)):
                            raise ValueError("Provider returned an invalid model catalog")
                        check_cancelled()
                        return payload
                    retry_status = response.status
                    retry_header = response.getheader("Retry-After")
                except (OSError, http.client.HTTPException):
                    check_cancelled()
                finally:
                    if response is not None:
                        response.close()
                    if connection is not None:
                        self.unregister_upstream(connection)
                        connection.close()
                retries += 1
                delay = retry_after_seconds(retry_header, retries)
                if key_wide_failure(retry_status):
                    lease.runtime.defer_attempt(
                        attempt,
                        delay,
                        rate_limited=retry_status == 429,
                    )
                else:
                    deadline = time.monotonic() + delay
                    while time.monotonic() < deadline:
                        check_cancelled()
                        self.stopping.wait(
                            min(0.25, deadline - time.monotonic())
                        )
        finally:
            lease.release()

    def snapshot(self):
        snapshot = self.metrics.snapshot()
        providers = self.providers()
        active = next(provider for provider in providers if provider.active)
        snapshot.update(
            rpm=active.effective_rpm,
            provider_rpm=active.rpm,
            key_rpm=active.key_rpm,
            queued=active.queued,
            cooldown_ms=active.cooldown_ms,
            key_configured=active.key_count > 0,
            providers=providers,
            config_error=self.config_error,
            history_error=self.history_error,
            tunnel_history_error=self.tunnel_history_error,
        )
        return snapshot

    def record(self, event: dict) -> None:
        if self.history:
            try:
                stored = self.history.record(event)
            except (OSError, sqlite3.Error):
                stored = False
            self.history_error = "" if stored is not False else "History write failed"

    def record_tunnel_event(self, event: dict) -> None:
        if self.tunnel_history:
            try:
                stored = self.tunnel_history.record(event)
            except (OSError, TypeError, ValueError, sqlite3.Error):
                stored = False
            self.tunnel_history_error = (
                "" if stored is not False else "Tunnel history write failed"
            )

    def history_stats(self, period: str):
        return self.history.stats(period) if self.history else {}

    def history_breakdown(self, period: str):
        return self.history.breakdown(period) if self.history else []

    def history_recent(self, period: str, limit: int = 100):
        return self.history.recent(period, limit) if self.history else []

    def shutdown(self) -> None:
        self.stopping.set()
        self.registry.close()
        self._abort_response_clients()
        self._abort_upstreams()
        if self.tunnel is not None:
            self.tunnel.stop()
        super().shutdown()

    def server_close(self) -> None:
        self.stopping.set()
        self.registry.close()
        self._abort_response_clients()
        self._abort_upstreams()
        if self.tunnel is not None:
            self.tunnel.stop()
        super().server_close()
        if self.history:
            self.history.close()
        if self.tunnel_history:
            self.tunnel_history.close()

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
import threading
import time
import unicodedata
from dataclasses import replace
from email.utils import parsedate_to_datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import unquote, urlsplit

from relay_config import ConfigError, ConfigStore
from relay_history import HistoryStore
from relay_tunnel import (
    DEFAULT_PUBLISHER_PROFILE,
    SharedTunnelControl,
    TunnelController,
    parse_publisher_profile,
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
MAX_REQUEST_BYTES = 64 * 1024 * 1024
MAX_INSPECT_BYTES = 4 * 1024 * 1024
MAX_ERROR_BYTES = 64 * 1024
ERROR_BODY_TIMEOUT = 2.0
TUNNEL_REQUEST_HEADER = "X-Provider-Switch-Tunnel"
MAX_TUNNEL_MODELS = 256
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


def _tunnel_settings(
    saved: dict | None,
) -> tuple[tuple[str, ...], str, int, str]:
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
    publisher_profile = raw.get(
        "publisher_profile", DEFAULT_PUBLISHER_PROFILE
    )
    try:
        parse_publisher_profile(publisher_profile)
    except ValueError as error:
        raise ValueError("Saved tunnel settings are invalid") from error
    return (
        _tunnel_models(raw.get("allowed_models", ())),
        token,
        rpm_per_ip,
        publisher_profile,
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
        json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
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
    return status is None or status in {401, 403, 429} or status >= 500


def _positive_int(value) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) and value > 0 else 0


class ResponseInspector:
    """Extract token usage without persisting response content."""

    def __init__(self, content_type: str):
        self._sse = "text/event-stream" in content_type.lower()
        self._json = "json" in content_type.lower()
        self._buffer = bytearray()
        self._usage = TokenUsage()
        self._terminal_at: float | None = None

    def feed(self, chunk: bytes) -> None:
        if not self._sse and not self._json:
            return
        if len(self._buffer) >= MAX_INSPECT_BYTES:
            return
        self._buffer.extend(chunk[: MAX_INSPECT_BYTES - len(self._buffer)])
        if self._sse:
            self._consume_sse_lines()

    @property
    def usage(self) -> TokenUsage:
        return self._usage

    @property
    def terminal_at(self) -> float | None:
        return self._terminal_at

    def _consume_sse_lines(self, final: bool = False) -> None:
        while True:
            index = self._buffer.find(b"\n")
            if index < 0:
                if final and self._buffer:
                    line, self._buffer = bytes(self._buffer), bytearray()
                else:
                    return
            else:
                line = bytes(self._buffer[:index])
                del self._buffer[: index + 1]
            line = line.rstrip(b"\r")
            if not line.startswith(b"data:"):
                continue
            data = line[5:].strip()
            if not data or data == b"[DONE]":
                continue
            try:
                payload = json.loads(data)
            except (json.JSONDecodeError, UnicodeDecodeError, RecursionError):
                continue
            self._merge_payload(payload)

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
        if (
            merged_usage
            and self._sse
            and payload.get("type")
            in {
                "message_delta",
                "response.completed",
                "response.failed",
                "response.incomplete",
            }
            and self._terminal_at is None
        ):
            self._terminal_at = time.monotonic()

    def finish(self) -> TokenUsage:
        if self._sse:
            self._consume_sse_lines(final=True)
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
    if isinstance(error, http.client.IncompleteRead):
        return "Provider closed the response early"
    if isinstance(error, http.client.HTTPException):
        return "Provider returned an invalid HTTP response"
    return "Provider connection failed"


class RelayHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "ProviderSwitch/2.0"
    sys_version = ""

    def _body(self):
        if self.headers.get("Transfer-Encoding"):
            self._error(501, "Chunked request bodies are not supported")
            return False, None
        lengths = self.headers.get_all("Content-Length", [])
        if not lengths:
            return True, None
        if len(set(lengths)) != 1:
            self._error(400, "Conflicting Content-Length headers")
            return False, None
        try:
            length = int(lengths[0])
        except ValueError:
            self._error(400, "Invalid Content-Length")
            return False, None
        if length < 0:
            self._error(400, "Invalid Content-Length")
            return False, None
        if length > MAX_REQUEST_BYTES:
            self._error(413, "Request body is too large")
            return False, None
        try:
            body = self.rfile.read(length)
        except OSError as error:
            raise ClientDisconnected from error
        return len(body) == length, body

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
        try:
            readable, _, _ = select.select([self.connection], [], [], 0)
            return bool(readable and self.connection.recv(1, socket.MSG_PEEK) == b"")
        except (OSError, ValueError):
            return True

    def _wait_retry(self, delay: float) -> None:
        deadline = time.monotonic() + delay
        while True:
            if self.server.stopping.is_set():
                raise RelayStopping
            if self._client_disconnected():
                raise ClientDisconnected
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return
            self.server.stopping.wait(min(0.25, remaining))

    def _request_headers(self, spec, target: str, configured_key: str | None) -> dict:
        blocked = HOP_HEADERS | {
            name.strip().lower()
            for name in self.headers.get("Connection", "").split(",")
            if name.strip()
        }
        blocked.add("content-length")
        blocked.add(TUNNEL_REQUEST_HEADER.lower())
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
        lease = self.server.registry.acquire_active()
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
        committed = False
        usage = TokenUsage()
        generation_started_at = started
        try:
            if (
                self.headers.get(TUNNEL_REQUEST_HEADER) == "1"
                and spec.auth_mode == "passthrough"
            ):
                self._error(503, "Tunnel route is unavailable")
                return
            ok, body = self._body()
            request_bytes = len(body or b"")
            self.server.metrics.add_bytes(bytes_in=request_bytes)
            if not ok:
                cancelled = self._response_status is None
                return
            is_json = (
                body
                and "json" in self.headers.get("Content-Type", "").lower()
                and not self.headers.get("Content-Encoding")
            )
            payload = json_object(body) if is_json else None
            model = request_model(payload)
            if spec.cache_1h and payload is not None:
                rewritten = echo_cache_for_one_hour(body, payload)
                cache_extended = rewritten is not body
                body = rewritten
            self.server.metrics.request(
                request_id, model, request_bytes, cache_extended
            )
            target = upstream_target(upstream, self.path)
            while True:
                attempt, waited = lease.runtime.acquire_attempt(
                    self._client_disconnected, model
                )
                queue_ms += waited
                response = None
                retry_status = None
                retry_header = None
                block_model = False
                balance_error = False
                try:
                    connection, attempt_target, proxy_headers = upstream_connection(
                        upstream, target, attempt.proxy_url
                    )
                    self.server.register_upstream(connection)
                    headers = self._request_headers(spec, target, attempt.api_key)
                    headers.update(proxy_headers)
                    connection.request(
                        self.command,
                        attempt_target,
                        body=body,
                        headers=headers,
                    )
                    generation_started_at = time.monotonic()
                    response = connection.getresponse()
                    if response.status < 400:
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
                except (OSError, http.client.HTTPException) as error:
                    retry_detail = network_error_detail(error)

                retries += 1
                retries_429 += int(retry_status == 429)
                delay = retry_after_seconds(retry_header, retries)
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
            self._response_status = response.status
            response_latency = (time.monotonic() - started) * 1000
            expected_length = (
                response.length
                if self.command != "HEAD" and response.status not in {204, 304}
                else None
            )
            self.server.metrics.response(
                request_id,
                spec.id,
                response.status,
                response_latency,
                cache_extended,
                generation_started_at,
            )
            self.send_response(response.status)
            blocked = HOP_HEADERS | {
                name.strip().lower()
                for name in (response.getheader("Connection") or "").split(",")
                if name.strip()
            }
            for name, value in response.getheaders():
                if name.lower() not in blocked and name.lower() != "content-length":
                    self.send_header(name, value)
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
            inspector = ResponseInspector(response.getheader("Content-Type") or "")
            if self.command != "HEAD" and response.status not in {204, 304}:
                while chunk := response.read1(64 * 1024):
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
        except (ClientDisconnected, RelayStopping):
            cancelled = True
            self.close_connection = True
            self._error_detail = "Client disconnected" if not self.server.stopping.is_set() else "Relay stopped"
        except (OSError, http.client.HTTPException) as error:
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
                if connection is not None:
                    self.server.unregister_upstream(connection)
                    connection.close()
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
    request_queue_size = 128
    daemon_threads = True

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
        self._upstreams = set()
        self._config_lock = threading.RLock()
        self._shared_tunnel_control = SharedTunnelControl()
        self._config_store = ConfigStore(config_path) if config and registry is None else None
        self.config_error = ""
        self.history_error = ""
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
            (
                self._tunnel_allowed_models,
                saved_tunnel_token,
                self._tunnel_rpm_per_ip,
                self._tunnel_publisher_profile,
            ) = _tunnel_settings(saved)
        except BaseException:
            self.registry.close()
            raise
        self._tunnel_token = saved_tunnel_token or secrets.token_urlsafe(32)
        self.history = None
        self.tunnel = None
        super().__init__(*args, **kwargs)
        try:
            self.tunnel = TunnelController(
                (LISTEN[0], self.server_port),
                self._tunnel_token,
                self._tunnel_allowed_models,
                sensitive_markers=self._tunnel_sensitive_markers(),
                secret_markers=self._tunnel_secret_markers(),
                route_guard=self._tunnel_route_safe,
                policy_lock=self._config_lock,
                public_rpm=self._tunnel_rpm_per_ip,
                publisher_profile=self._tunnel_publisher_profile,
            )
            if saved is not None and self._config_store is not None:
                self._config_store.save(self._config_value())
            if history is True:
                try:
                    self.history = HistoryStore()
                except (OSError, sqlite3.Error):
                    # History is useful telemetry, never a reason for the relay to fail.
                    self.history = None
                    self.history_error = "History unavailable"
            else:
                self.history = history or None
            recent = getattr(self.history, "recent", None)
            if recent is not None:
                try:
                    self.metrics.restore_recent(recent("all", 100))
                except (KeyError, OSError, TypeError, ValueError, sqlite3.Error):
                    self.history_error = "History read failed"
        except BaseException:
            if self.tunnel is not None:
                self.tunnel.stop()
            self.registry.close()
            super().server_close()
            raise

    def _config_value(self) -> dict:
        value = self.registry.export_config(self._environment_key)
        value["tunnel"] = {
            "allowed_models": list(self._tunnel_allowed_models),
            "access_token": self._tunnel_token,
            "rpm_per_ip": self._tunnel_rpm_per_ip,
            "publisher_profile": self._tunnel_publisher_profile,
        }
        return value

    def _tunnel_sensitive_markers(self) -> tuple[str, ...]:
        markers = {self._tunnel_token, self._environment_key}
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
        markers = {self._tunnel_token, self._environment_key}
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
            return (
                not self.stopping.is_set()
                and self.registry.active().auth_mode != "passthrough"
            )
        except (KeyError, RuntimeError):
            return False

    def _sync_tunnel_security(self) -> None:
        if self.tunnel is not None:
            self.tunnel.configure(
                token=self._tunnel_token,
                allowed_models=self._tunnel_allowed_models,
                sensitive_markers=self._tunnel_sensitive_markers(),
                secret_markers=self._tunnel_secret_markers(),
                public_rpm=self._tunnel_rpm_per_ip,
                publisher_profile=self._tunnel_publisher_profile,
            )

    def _persist(self) -> None:
        if self._config_store is None:
            return
        try:
            self._config_store.save(self._config_value())
        except ConfigError:
            self.config_error = "Settings changed for this session but were not saved"
        else:
            self.config_error = ""

    def register_upstream(self, connection) -> None:
        with self._upstream_lock:
            if self.stopping.is_set():
                connection.close()
                raise RelayStopping
            self._upstreams.add(connection)

    def unregister_upstream(self, connection) -> None:
        with self._upstream_lock:
            self._upstreams.discard(connection)

    def _abort_upstreams(self) -> None:
        with self._upstream_lock:
            connections = tuple(self._upstreams)
            self._upstreams.clear()
        for connection in connections:
            sock = getattr(connection, "sock", None)
            if sock is not None:
                try:
                    sock.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass
                try:
                    socket.close(sock.detach())
                except OSError:
                    pass
            connection.close()

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
        with self._config_lock:
            previous_id = self.registry.active().id
            selected = self.registry.select(provider)
            changed = selected.id != previous_id
            if changed:
                self._tunnel_allowed_models = ()
                self._sync_tunnel_security()
            self._persist()
        if changed:
            self.tunnel.stop()
        return selected.name, urlsplit(selected.upstream)

    def toggle(self):
        with self._config_lock:
            previous_id = self.registry.active().id
            selected = self.registry.toggle()
            changed = selected.id != previous_id
            if changed:
                self._tunnel_allowed_models = ()
                self._sync_tunnel_security()
            self._persist()
        if changed:
            self.tunnel.stop()
        return selected.name, urlsplit(selected.upstream)

    def add_provider(self, **values):
        with self._config_lock:
            provider = self.registry.add(**values)
            self._sync_tunnel_security()
            self._persist()
            return provider

    def update_provider(self, provider_id, **values):
        with self._config_lock:
            provider = self.registry.update(provider_id, **values)
            self._sync_tunnel_security()
            self._persist()
            return provider

    def delete_provider(self, provider_id):
        with self._config_lock:
            provider = self.registry.delete(provider_id)
            self._sync_tunnel_security()
            self._persist()
            return provider

    def add_provider_key(self, provider_id, api_key, rpm=0, proxy_url=""):
        with self._config_lock:
            fingerprint = self.registry.add_key(provider_id, api_key, rpm, proxy_url)
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
            self._sync_tunnel_security()
            self._persist()
            return result

    def update_provider_key(self, provider_id, fingerprint, rpm, proxy_url=None):
        if (
            provider_id == "echo"
            and self._environment_key
            and fingerprint == key_fingerprint(self._environment_key)
        ):
            raise ValueError("FREEMODEL_API_KEY is fixed at 30 RPM and Direct")
        with self._config_lock:
            result = self.registry.update_key(
                provider_id, fingerprint, rpm, proxy_url
            )
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
            self._tunnel_allowed_models = selected
            self._sync_tunnel_security()
            self._persist()
            return self._tunnel_allowed_models

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
                raise ValueError("The active provider cannot be shared through a tunnel")
            self._sync_tunnel_security()
            self._persist()
        self.tunnel.start()
        return self.tunnel_snapshot()

    def stop_tunnel(self) -> dict:
        self.tunnel.stop()
        return self.tunnel_snapshot()

    def fetch_models(self, provider_id):
        lease = self.registry.acquire(provider_id)
        try:
            spec = lease.spec
            upstream = spec.parsed_upstream
            target = upstream_target(upstream, "/v1/models")
            retries = 0
            while True:
                attempt, _waited = lease.runtime.acquire_attempt(
                    self.stopping.is_set, ""
                )
                connection = response = None
                retry_status = None
                retry_header = None
                try:
                    connection, request_target, proxy_headers = upstream_connection(
                        upstream, target, attempt.proxy_url, timeout=15
                    )
                    self.register_upstream(connection)
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
                    response = connection.getresponse()
                    if response.status < 400:
                        body = response.read(MAX_INSPECT_BYTES + 1)
                        if len(body) > MAX_INSPECT_BYTES:
                            raise ValueError("Model catalog is too large")
                        payload = json.loads(body)
                        if not isinstance(payload, (dict, list)):
                            raise ValueError("Provider returned an invalid model catalog")
                        return payload
                    retry_status = response.status
                    retry_header = response.getheader("Retry-After")
                except (OSError, http.client.HTTPException):
                    pass
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
                elif self.stopping.wait(delay):
                    raise RelayStopping
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
        )
        return snapshot

    def record(self, event: dict) -> None:
        if self.history:
            try:
                stored = self.history.record(event)
            except (OSError, sqlite3.Error):
                stored = False
            self.history_error = "" if stored is not False else "History write failed"

    def history_stats(self, period: str):
        return self.history.stats(period) if self.history else {}

    def history_breakdown(self, period: str):
        return self.history.breakdown(period) if self.history else []

    def history_recent(self, period: str, limit: int = 100):
        return self.history.recent(period, limit) if self.history else []

    def shutdown(self) -> None:
        self.stopping.set()
        if self.tunnel is not None:
            self.tunnel.stop()
        self.registry.close()
        self._abort_upstreams()
        super().shutdown()

    def server_close(self) -> None:
        self.stopping.set()
        if self.tunnel is not None:
            self.tunnel.stop()
        self.registry.close()
        self._abort_upstreams()
        super().server_close()
        if self.history:
            self.history.close()

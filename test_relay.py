import base64
import http.client
import io
import json
import os
import socket
import sqlite3
import struct
import subprocess
import tempfile
import threading
import time
import unicodedata
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import Mock, patch
from urllib.parse import urlsplit

import main as relay
import relay_http
import relay_runtime
import relay_tunnel
from relay_config import ConfigError
from relay_history import HistoryStore
from relay_http import model_unavailable_on_plan, upstream_connection
from relay_tunnel import TunnelController, TunnelGateway
from relay_ui import PALETTE, RelayApp, _state_cell
from textual.widgets import Button, DataTable, Input, OptionList, SelectionList, Static


class UpstreamHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        if self.path != "/v1/models":
            self.send_error(404)
            return
        body = json.dumps(
            {
                "data": [
                    {"id": "gpt-test"},
                    {"id": "claude-sonnet-test"},
                    {"id": "deepseek-test"},
                ]
            }
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        if hasattr(self.server, "auth_sequence"):
            self.server.auth_sequence.append(self.headers.get("Authorization"))
        if (
            self.path == "/v1/model-fallback"
            and self.headers.get("Authorization") == "Bearer lite-key"
            and json.loads(body).get("model") == "gpt-5.6-sol"
        ):
            result = (
                b"Unexpected status 404 Not Found: Model 'gpt-5.6-sol' "
                b"is not available on your plan."
            )
            self.send_response(404)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Transfer-Encoding", "chunked")
            self.send_header("Connection", "close")
            self.end_headers()
            midpoint = len(result) // 2
            for chunk in (result[:midpoint], result[midpoint:]):
                self.wfile.write(f"{len(chunk):x}\r\n".encode() + chunk + b"\r\n")
            self.wfile.write(b"0\r\n\r\n")
            return
        if (
            self.path == "/v1/balance-fallback"
            and self.headers.get("Authorization") == "Bearer lite-key"
        ):
            result = b'{"error":{"code":"insufficient_balance"}}'
            self.send_response(402)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(result)))
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(result)
            return
        if self.path == "/v1/reset":
            self.server.reset_count = getattr(self.server, "reset_count", 0) + 1
            if self.server.reset_count == 1:
                self.connection.setsockopt(
                    socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("hh", 1, 0)
                )
                socket.close(self.connection.detach())
                self.close_connection = True
                return
        if self.path == "/v1/truncated":
            self.send_response(200)
            self.send_header("Content-Length", "20")
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(b"short")
            return
        if self.path == "/v1/stream":
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(b"data: first\n\n")
            self.wfile.flush()
            time.sleep(0.75)
            self.wfile.write(b"data: second\n\n")
            self.wfile.write(
                b'data: {"type":"response.completed","response":{"usage":'
                b'{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}\n\n'
            )
            return
        if self.path == "/v1/token-speed":
            time.sleep(0.12)
            result = b'{"usage":{"input_tokens":10,"output_tokens":24,"total_tokens":34}}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(result)))
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(result)
            return
        if self.path == "/v1/framing":
            self.send_response(200)
            self.send_header("Transfer-Encoding", "chunked")
            self.send_header("Content-Length", "1")
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(b"5\r\nhello\r\n0\r\n\r\n")
            return
        if self.path == "/v1/retry":
            self.server.retry_count = getattr(self.server, "retry_count", 0) + 1
            if self.server.retry_count <= 2:
                self.send_response(429)
                self.send_header("Retry-After", "0.05")
                self.send_header("Content-Length", "0")
                self.send_header("Connection", "close")
                self.end_headers()
                return
        if self.path == "/v1/always-429":
            self.send_response(429)
            self.send_header("Retry-After", "60")
            self.send_header("Content-Length", "0")
            self.send_header("Connection", "close")
            self.end_headers()
            return
        if self.path == "/v1/hang":
            self.server.hang_started.set()
            self.server.hang_release.wait(5)
            self.close_connection = True
            return
        if self.path == "/v1/bad-request":
            self.server.bad_request_count = getattr(self.server, "bad_request_count", 0) + 1
            self.send_response(400)
            self.send_header("Retry-After", "2")
            self.send_header("Content-Length", "0")
            self.send_header("Connection", "close")
            self.end_headers()
            return
        if self.path == "/v1/rejected":
            self.server.rejected_count = getattr(self.server, "rejected_count", 0) + 1
            if self.server.rejected_count <= 2:
                result = b'{"error":"rejected"}'
                self.send_response(401)
                self.send_header("Retry-After", "0.01")
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(result)))
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(result)
                return
        if self.path.startswith("/v1/queue/"):
            with self.server.arrival_lock:
                self.server.arrivals.append(time.monotonic())

        result = json.dumps(
            {
                "provider": self.server.label,
                "path": self.path,
                "authorization": self.headers.get("Authorization"),
                "x_api_key": self.headers.get("x-api-key"),
                "removed": self.headers.get("X-Remove"),
                "body": json.loads(body),
            }
        ).encode()
        self.send_response(201)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(result)))
        self.send_header("Connection", "X-Private")
        self.send_header("X-Private", "must-not-pass")
        self.end_headers()
        self.wfile.write(result)

    def log_message(self, _format, *_args):
        pass


class OutboundProxyHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def _record(self, body=b""):
        self.server.requests.append(
            {
                "method": self.command,
                "target": self.path,
                "authorization": self.headers.get("Authorization"),
                "proxy_authorization": self.headers.get("Proxy-Authorization"),
                "body": body,
            }
        )

    def do_GET(self):
        self._record()
        if getattr(self.server, "get_failures", 0):
            self.server.get_failures -= 1
            self.send_response(429)
            self.send_header("Retry-After", "0.01")
            self.send_header("Content-Length", "0")
            self.send_header("Connection", "close")
            self.end_headers()
            return
        result = b'{"data":[{"id":"proxied-model"}]}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(result)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(result)

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self._record(body)
        result = json.dumps(
            {
                "through_proxy": True,
                "authorization": self.headers.get("Authorization"),
            }
        ).encode()
        self.send_response(201)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(result)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(result)

    def log_message(self, _format, *_args):
        pass


class TunnelRelayHandler(BaseHTTPRequestHandler):
    """Hostile local relay used to prove the public gateway fails closed."""

    protocol_version = "HTTP/1.1"

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self.server.requests.append(
            {
                "path": self.path,
                "headers": {name.casefold(): value for name, value in self.headers.items()},
                "body": body,
            }
        )
        try:
            payload = json.loads(body)
        except json.JSONDecodeError:
            payload = {}
        case = payload.get("test_case", "safe-json")
        status = 200
        content_type = "application/json"
        encoding = ""
        if case == "safe-json":
            response_body = json.dumps(
                {"id": "safe-response", "model": payload.get("model"), "output": []}
            ).encode()
        elif case == "safe-created":
            status = 201
            response_body = json.dumps(
                {"id": "safe-response", "model": payload.get("model"), "output": []}
            ).encode()
        elif case == "echo-value":
            response_body = json.dumps(
                {"model": payload.get("model"), "output": payload.get("value")},
                ensure_ascii=False,
            ).encode()
        elif case == "echo-twice":
            response_body = json.dumps(
                {
                    "model": payload.get("model"),
                    "output": [payload.get("value"), payload.get("value")],
                },
                ensure_ascii=False,
            ).encode()
        elif case == "echo-decoded":
            decoded = {
                "%45choGate": "EchoGate",
                "%4ftherGate": "OtherGate",
            }.get(payload.get("value"), payload.get("value"))
            response_body = json.dumps(
                {"model": payload.get("model"), "output": decoded},
                ensure_ascii=False,
            ).encode()
        elif case == "unicode-leak":
            response_body = json.dumps(
                {
                    "model": payload.get("model"),
                    "routing_hint": "Cafe\u0301AI",
                },
                ensure_ascii=False,
            ).encode()
        elif case == "unicode-b64-leak":
            response_body = json.dumps(
                {"model": payload.get("model"), "routing_hint": "Q2FmZcyBQUk"}
            ).encode()
        elif case == "idn-leak":
            response_body = json.dumps(
                {
                    "model": payload.get("model"),
                    "routing_hint": "xn--caf-dma.example",
                }
            ).encode()
        elif case == "ip-leak":
            response_body = json.dumps(
                {
                    "model": payload.get("model"),
                    "routing_hint": "2001:0db8:0000:0000:0000:0000:0000:0001",
                }
            ).encode()
        elif case == "leak-json":
            response_body = json.dumps(
                {
                    "provider": self.server.provider_marker,
                    "key": self.server.key_marker,
                    "url": self.server.url_marker,
                    "routing_hint": self.server.provider_marker,
                }
            ).encode()
        elif case == "split-leak-json":
            response_body = json.dumps(
                {"output": ["SentinelProvider-", "DoNotExpose"]},
                separators=(",", ":"),
            ).encode()
        elif case == "separator-leak-json":
            response_body = json.dumps(
                {"output": "Sentinel Provider DoNotExpose"}
            ).encode()
        elif case == "percent-leak-json":
            response_body = b'{"output":"E%63hoGate"}'
        elif case == "typed-split-json":
            response_body = json.dumps(
                {
                    "output": [
                        {"text": "SentinelProvider-", "type": "output_text"},
                        {"text": "DoNotExpose", "type": "output_text"},
                    ]
                },
                separators=(",", ":"),
            ).encode()
        elif case == "safe-sse":
            content_type = "text/event-stream"
            response_body = (
                b'data: {"type":"response.output_text.delta","delta":"safe"}\n\n'
                b"data: [DONE]\n\n"
            )
        elif case == "leak-sse":
            content_type = "text/event-stream"
            response_body = (
                b'data: {"type":"response.output_text.delta","delta":"safe"}\n\n'
                + f'data: {{"delta":"{self.server.key_marker}"}}\n\n'.encode()
            )
        elif case == "split-leak-sse":
            content_type = "text/event-stream"
            response_body = (
                b'data: {"delta":"sentinel-key-"}\n\n'
                b'data: {"delta":"DoNotExpose"}\n\n'
            )
        elif case == "typed-split-sse":
            content_type = "text/event-stream"
            response_body = (
                b'data: {"type":"response.output_text.delta",'
                b'"delta":"sentinel-key-"}\n\n'
                b'data: {"type":"response.output_text.delta",'
                b'"delta":"DoNotExpose"}\n\n'
            )
        elif case == "percent-leak-sse":
            content_type = "text/event-stream"
            response_body = b'data: {"delta":"%45choGate"}\n\n'
        elif case == "transport-metadata":
            response_body = json.dumps(
                {
                    "model": payload.get("model"),
                    "output": [],
                    "backend": "hidden",
                    "vendor": "hidden",
                    "endpoint": "hidden",
                    "server": "hidden",
                    "api_key": "untracked-api-secret",
                    "access_token": "untracked-access-secret",
                    "private_key": "untracked-private-secret",
                    "session_token": "untracked-session-secret",
                    "organization_id": "untracked-organization",
                    "request_id": "untracked-request",
                    "headers": {
                        "Authorization": "untracked-authorization",
                        "Via": "untracked-transport",
                    },
                }
            ).encode()
        elif case == "transport-metadata-sse":
            content_type = "text/event-stream"
            response_body = (
                b'data: {"delta":"safe","token":"untracked-token",'
                b'"response_headers":{"Via":"untracked-transport"}}\n\n'
            )
        elif case == "usage-json":
            response_body = b'{"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}'
        elif case == "large-json":
            response_body = b'{"output":"' + (b"x" * (8 * 1024 * 1024)) + b'"}'
        elif case == "retired-marker":
            response_body = b'{"output":"retired-secret"}'
        elif case == "malformed-sse":
            content_type = "text/event-stream"
            response_body = b'data: {"broken":\n\n'
        elif case == "encoded":
            encoding = "gzip"
            response_body = b"not-a-safe-transparent-response"
        elif case == "malformed-json":
            response_body = b'{"broken":'
        elif case == "nan-json":
            response_body = b'{"value":NaN}'
        elif case == "surrogate-json":
            response_body = b'{"model":"allowed-model","output":"\\ud800"}'
        elif case == "surrogate-sse":
            content_type = "text/event-stream"
            response_body = b'data: {"delta":"\\ud800"}\n\n'
        else:
            status = 401
            response_body = json.dumps(
                {"error": self.server.provider_marker, "key": self.server.key_marker}
            ).encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(response_body)))
        self.send_header("X-Provider", self.server.provider_marker)
        self.send_header("X-Upstream-URL", self.server.url_marker)
        self.send_header("X-Upstream-Key", self.server.key_marker)
        if encoding:
            self.send_header("Content-Encoding", encoding)
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(response_body)

    def log_message(self, _format, *_args):
        pass


class FakeSshProcess:
    def __init__(self, output):
        self.stdout = io.StringIO(output)
        self.done = threading.Event()
        self.terminated = self.killed = False

    def poll(self):
        return 0 if self.done.is_set() else None

    def wait(self, timeout=None):
        if not self.done.wait(timeout):
            raise subprocess.TimeoutExpired("ssh", timeout)
        return 0

    def terminate(self):
        self.terminated = True
        self.done.set()

    def kill(self):
        self.killed = True
        self.done.set()


class StubbornSshProcess(FakeSshProcess):
    def terminate(self):
        self.terminated = True

    def wait(self, timeout=None):
        if timeout is not None and not self.killed:
            raise subprocess.TimeoutExpired("ssh", timeout)
        return super().wait(timeout)


class RejectedSshProcess(FakeSshProcess):
    def __init__(self):
        super().__init__("")
        self.done.set()

    def poll(self):
        return 255

    def wait(self, timeout=None):
        return 255


def start_server(handler, label=None, server_class=ThreadingHTTPServer, **kwargs):
    server = server_class(("127.0.0.1", 0), handler, **kwargs)
    server.label = label
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def provider_registry(
    local_upstream,
    echo_upstream=None,
    *,
    echo_api_key="",
    echo_rpm=0,
):
    local = relay.ProviderRegistry.make_spec(
        "Local",
        local_upstream,
        auth_mode="passthrough",
        provider_id="local",
    )
    echo = relay.ProviderRegistry.make_spec(
        "EchoGate",
        echo_upstream or local_upstream,
        auth_mode="auto",
        cache_1h=True,
        rpm=echo_rpm,
        provider_id="echo",
    )
    keys = (echo_api_key,) if echo_api_key else ()
    return relay.ProviderRegistry(((local, ()), (echo, keys)))


def start_proxy(local_upstream, echo_upstream=None, **registry_options):
    return start_server(
        relay.RelayHandler,
        server_class=relay.RelayServer,
        registry=provider_registry(
            local_upstream,
            echo_upstream,
            **registry_options,
        ),
        history=False,
    )


class TunnelGatewayTest(unittest.TestCase):
    TOKEN = "share-token-0000000000000000000000000000000000000000"
    MODELS = ("allowed-model", "another-model")
    MARKERS = (
        "SentinelProvider-DoNotExpose",
        "sentinel-key-DoNotExpose",
        "https://sentinel.invalid/v1",
    )
    PROFILE = relay_tunnel.DEFAULT_PUBLISHER_PROFILE
    PUBLIC_URL = relay_tunnel.publisher_url(PROFILE)
    IDENTITY = r"C:\Users\tester\AppData\Local\ProviderSwitchboard\ssh\model-tunnel_ed25519"

    @unittest.skipUnless(
        relay_tunnel._find_ssh() and "PROGRAMDATA" in os.environ,
        "Windows OpenSSH is required",
    )
    def test_child_environment_starts_windows_ssh_without_secrets(self):
        environment = relay_tunnel._child_environment()
        result = subprocess.run(
            [relay_tunnel._find_ssh(), "-V"],
            env=environment,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=5,
            creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
        )

        self.assertEqual(result.returncode, 0)
        self.assertIn(b"OpenSSH_", result.stdout + result.stderr)
        self.assertIn("PROGRAMDATA", environment)
        self.assertNotIn("FREEMODEL_API_KEY", environment)
        self.assertNotIn("SSH_AUTH_SOCK", environment)
    KNOWN_HOSTS = r"C:\Users\tester\AppData\Local\ProviderSwitchboard\ssh\model-tunnel_known_hosts"

    @classmethod
    def setUpClass(cls):
        cls.relay_server = start_server(TunnelRelayHandler)
        cls.relay_server.requests = []
        (
            cls.relay_server.provider_marker,
            cls.relay_server.key_marker,
            cls.relay_server.url_marker,
        ) = cls.MARKERS
        cls.gateway = TunnelGateway(
            cls.relay_server.server_address,
            cls.TOKEN,
            cls.MODELS,
            sensitive_markers=cls.MARKERS,
            secret_markers=(cls.MARKERS[1],),
        )
        cls.gateway_thread = threading.Thread(
            target=cls.gateway.serve_forever, daemon=True
        )
        cls.gateway_thread.start()

    @classmethod
    def tearDownClass(cls):
        cls.gateway.shutdown()
        cls.gateway.server_close()
        cls.gateway_thread.join(timeout=1)
        cls.relay_server.shutdown()
        cls.relay_server.server_close()

    def setUp(self):
        self.relay_server.requests.clear()
        self.gateway.configure(
            token=self.TOKEN,
            allowed_models=self.MODELS,
            sensitive_markers=self.MARKERS,
            secret_markers=(self.MARKERS[1],),
            public_rpm=0,
        )

    @staticmethod
    def _call(address, method, path, body=None, token=TOKEN, headers=None):
        request_headers = dict(headers or {})
        request_headers.setdefault(
            relay_tunnel.CLIENT_IP_HEADER, "203.0.113.10"
        )
        if token is not None:
            request_headers.setdefault("Authorization", f"Bearer {token}")
        if isinstance(body, dict):
            body = json.dumps(body).encode()
            request_headers.setdefault("Content-Type", "application/json")
        connection = http.client.HTTPConnection(*address, timeout=2)
        connection.request(method, path, body=body, headers=request_headers)
        response = connection.getresponse()
        result = (response.status, response.getheaders(), response.read())
        connection.close()
        return result

    def _request(self, method, path, body=None, token=TOKEN, headers=None):
        return self._call(
            self.gateway.server_address, method, path, body, token, headers
        )

    def assertNoSecrets(self, headers, body):
        wire = ("\n".join(f"{name}: {value}" for name, value in headers)).encode()
        for secret in (*self.MARKERS, self.TOKEN):
            self.assertNotIn(secret.encode(), wire + body)

    def test_auth_synthetic_models_and_token_is_never_forwarded(self):
        for token in (None, "wrong-token"):
            status, headers, body = self._request(
                "POST", "/v1/responses", {"model": "allowed-model"}, token
            )
            self.assertEqual((status, json.loads(body)), (401, {"error": "Unauthorized"}))
            self.assertNoSecrets(headers, body)
        self.assertEqual(self.relay_server.requests, [])

        status, headers, body = self._request(
            "GET",
            "/v1/models",
            token=None,
            headers={"x-api-key": self.TOKEN},
        )
        self.assertEqual(status, 200)
        self.assertEqual(
            {item["id"] for item in json.loads(body)["data"]}, set(self.MODELS)
        )
        self.assertEqual(self.relay_server.requests, [])
        self.assertNoSecrets(headers, body)

        status, headers, body = self._request(
            "POST",
            "/v1/responses",
            {"model": "allowed-model", "test_case": "safe-json"},
            headers={"X-Provider-Switch-Tunnel": "attacker"},
        )
        self.assertEqual(status, 200)
        request = self.relay_server.requests[-1]
        self.assertEqual(request["headers"].get("x-provider-switch-tunnel"), "1")
        self.assertNotIn("authorization", request["headers"])
        self.assertNotIn("x-api-key", request["headers"])
        self.assertNotIn(self.TOKEN.encode(), request["body"])
        self.assertEqual(
            {name.casefold() for name, _value in headers},
            {"content-type", "content-length", "connection"},
        )
        self.assertNoSecrets(headers, body)
        for path in ("/v1/chat/completions", "/v1/completions", "/v1/messages"):
            status, headers, body = self._request(
                "POST",
                path,
                {"model": "allowed-model", "test_case": "safe-json"},
            )
            self.assertEqual(status, 200)
            self.assertNoSecrets(headers, body)

        status, headers, body = self._request(
            "POST",
            "/v1/responses",
            {"model": "allowed-model", "test_case": "safe-created"},
        )
        self.assertEqual(status, 200)
        self.assertNoSecrets(headers, body)

    def test_duplicate_public_auth_headers_are_rejected(self):
        for header in ("Authorization", "x-api-key"):
            with self.subTest(header=header):
                connection = http.client.HTTPConnection(
                    *self.gateway.server_address, timeout=2
                )
                connection.putrequest("GET", "/v1/models")
                if header == "Authorization":
                    connection.putheader(header, f"Bearer {self.TOKEN}")
                    connection.putheader(header, "Bearer wrong-token")
                else:
                    connection.putheader(header, self.TOKEN)
                    connection.putheader(header, "wrong-token")
                connection.endheaders()
                response = connection.getresponse()
                body = response.read()
                connection.close()
                self.assertEqual(
                    (response.status, json.loads(body)),
                    (401, {"error": "Unauthorized"}),
                )

    def test_allowlist_and_routes_are_rejected_before_relay(self):
        for model in ("blocked-model", "Allowed-model", "allowed-model-extra"):
            status, _headers, body = self._request(
                "POST", "/v1/responses", {"model": model}
            )
            self.assertEqual((status, json.loads(body)), (403, {"error": "Request rejected"}))
        for body in ({}, {"model": 123}, b'{"model":'):
            status, _headers, result = self._request(
                "POST", "/v1/responses", body
            )
            self.assertEqual((status, json.loads(result)), (400, {"error": "Invalid request"}))
        for method, path, expected in (
            ("GET", "/v1/responses", 404),
            ("POST", "/v1/models", 404),
            ("PUT", "/v1/responses", 405),
            ("DELETE", "/v1/responses", 405),
            ("PATCH", "/v1/responses", 405),
            ("OPTIONS", "/v1/responses", 405),
            ("POST", "/v1/responses?debug=1", 404),
            ("POST", "/v1/%72esponses", 404),
            ("POST", "/v1/responses/", 404),
            ("POST", "/internal/status", 404),
        ):
            status, _headers, body = self._request(
                method, path, {"model": "allowed-model"}
            )
            self.assertEqual(status, expected)
            self.assertEqual(
                json.loads(body),
                {"error": "Method not allowed" if expected == 405 else "Not found"},
            )
        status, _headers, body = self._request(
            "HEAD", "/v1/responses", {"model": "allowed-model"}
        )
        self.assertEqual((status, body), (405, b""))
        self.assertEqual(self.relay_server.requests, [])

    def test_model_id_containing_private_marker_is_never_exposed(self):
        private_model = f"model-{self.MARKERS[0]}"
        self.gateway.configure(
            allowed_models=(private_model,), sensitive_markers=self.MARKERS
        )
        status, headers, body = self._request("GET", "/v1/models")
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(body)["data"], [])
        self.assertNoSecrets(headers, body)

        status, headers, body = self._request(
            "POST", "/v1/responses", {"model": private_model}
        )
        self.assertEqual((status, json.loads(body)), (403, {"error": "Request rejected"}))
        self.assertNoSecrets(headers, body)
        self.assertEqual(self.relay_server.requests, [])

    def test_hostile_headers_json_sse_encoding_and_malformed_fail_closed(self):
        for case, expected in (
            ("safe-json", 200),
            ("safe-sse", 200),
            ("leak-json", 502),
            ("leak-sse", 502),
            ("encoded", 502),
            ("malformed-json", 502),
            ("nan-json", 502),
            ("malformed-sse", 502),
            ("surrogate-json", 502),
            ("surrogate-sse", 502),
            ("non-2xx", 502),
        ):
            with self.subTest(case=case):
                status, headers, body = self._request(
                    "POST",
                    "/v1/responses",
                    {
                        "model": "allowed-model",
                        "stream": case.endswith("sse"),
                        "test_case": case,
                    },
                )
                self.assertEqual(status, expected)
                self.assertNoSecrets(headers, body)
                if expected == 502:
                    self.assertEqual(json.loads(body), {"error": "Upstream response rejected"})
        for request in self.relay_server.requests:
            self.assertEqual(request["headers"].get("x-provider-switch-tunnel"), "1")
            self.assertNotIn("authorization", request["headers"])
            self.assertNotIn("x-api-key", request["headers"])

    def test_semantic_fragments_cannot_split_markers_across_json_or_sse(self):
        for case, expected in (
            ("split-leak-json", 200),
            ("split-leak-sse", 502),
            ("separator-leak-json", 200),
            ("typed-split-json", 200),
            ("typed-split-sse", 502),
        ):
            with self.subTest(case=case):
                status, headers, body = self._request(
                    "POST",
                    "/v1/responses",
                    {
                        "model": "allowed-model",
                        "stream": case.endswith("sse"),
                        "test_case": case,
                    },
                )
                self.assertEqual(status, expected)
                if expected == 502:
                    self.assertEqual(
                        json.loads(body),
                        {"error": "Upstream response rejected"},
                    )
                    self.assertNoSecrets(headers, body)
                else:
                    self.assertIn(b"DoNotExpose", body)

        status, _headers, body = self._request(
            "POST",
            "/v1/responses",
            {"model": "allowed-model", "test_case": "transport-metadata"},
        )
        self.assertEqual(status, 200)
        clean = json.loads(body)
        for field in (
            "backend",
            "vendor",
            "endpoint",
            "server",
            "api_key",
            "access_token",
            "private_key",
            "session_token",
            "organization_id",
            "request_id",
            "headers",
        ):
            self.assertNotIn(field, clean)
        self.assertNotIn(b"untracked", body)

        status, _headers, body = self._request(
            "POST",
            "/v1/responses",
            {
                "model": "allowed-model",
                "stream": True,
                "test_case": "transport-metadata-sse",
            },
        )
        self.assertEqual(status, 200)
        self.assertIn(b'"delta":"safe"', body)
        self.assertNotIn(b"untracked", body)

        status, _headers, body = self._request(
            "POST",
            "/v1/responses",
            {"model": "allowed-model", "test_case": "usage-json"},
        )
        self.assertEqual(status, 200)
        self.assertEqual(
            json.loads(body)["usage"],
            {"input_tokens": 3, "output_tokens": 5, "total_tokens": 8},
        )

    def test_current_and_retired_public_tokens_are_rejected_pre_relay(self):
        before = len(self.relay_server.requests)
        current = "rotated-share-token-1111111111111111111111111111111111111111"
        self.gateway.configure(token=current)
        try:
            for secret in (self.TOKEN, current):
                midpoint = len(secret) // 2
                for body in (
                    {"model": "allowed-model", "value": secret},
                    {
                        "model": "allowed-model",
                        "parts": [secret[:midpoint], secret[midpoint:]],
                    },
                    {
                        "model": "allowed-model",
                        "value": "".join(f"%{byte:02X}" for byte in secret.encode()),
                    },
                    {
                        "model": "allowed-model",
                        "value": "".join(
                            f"%2525{byte:02X}" for byte in secret.encode()
                        ),
                    },
                    {
                        "model": "allowed-model",
                        "value": base64.b64encode(secret.encode()).decode(),
                    },
                ):
                    with self.subTest(secret=secret[:7], body=tuple(body)):
                        status, _headers, _body = self._request(
                            "POST", "/v1/responses", body, token=current
                        )
                        self.assertEqual(status, 403)

                status, _headers, _body = self._request(
                    "POST",
                    "/v1/responses",
                    {"model": "allowed-model"},
                    token=current,
                    headers={"Anthropic-Beta": secret},
                )
                self.assertEqual(status, 403)

            midpoint = len(current) // 2
            status, _headers, _body = self._request(
                "POST",
                "/v1/responses",
                {"model": "allowed-model", "piece": current[midpoint:]},
                token=current,
                headers={"Anthropic-Beta": current[:midpoint]},
            )
            self.assertEqual(status, 403)

            duplicate = (
                '{"model":"allowed-model","note":"'
                + current
                + '","note":"safe"}'
            ).encode()
            status, _headers, _body = self._request(
                "POST",
                "/v1/responses",
                duplicate,
                token=current,
                headers={"Content-Type": "application/json"},
            )
            self.assertEqual(status, 400)
        finally:
            self.gateway.configure(token=self.TOKEN)
        self.assertEqual(len(self.relay_server.requests), before)

    def test_encoded_identity_is_not_a_request_or_response_oracle(self):
        before = len(self.relay_server.requests)
        cases = (
            ("xy", ["x", "y"]),
            ("a+b", ["a%2B", "b"]),
            (
                "EchoGate",
                ["%45%63%68%6F%47%61%74%65"],
            ),
            ("EchoGate", ["%45choGate"]),
            ("EchoGate", ["E%63hoGate"]),
            ("EchoGate", ["Echo%47ate"]),
            (
                "EchoGate",
                ["%2545%2563%2568%256F%2547%2561%2574%2565"],
            ),
            (
                "EchoGate",
                ["%252545%252563%252568%25256F%252547%252561%252574%252565"],
            ),
        )
        responses = []
        for marker, parts in cases:
            with self.subTest(marker=marker):
                self.gateway.configure(sensitive_markers=(marker,))
                status, _headers, body = self._request(
                    "POST",
                    "/v1/responses",
                    {"model": "allowed-model", "parts": parts},
                )
                self.assertEqual(status, 200)
                responses.append(body)
        self.assertEqual(len(set(responses)), 1)
        self.assertEqual(len(self.relay_server.requests), before + len(cases))

        self.gateway.configure(
            allowed_models=("safe-model", "model-E%63hoGate"),
            sensitive_markers=("EchoGate",),
        )
        status, _headers, body = self._request("GET", "/v1/models")
        self.assertEqual(status, 200)
        self.assertEqual(
            [item["id"] for item in json.loads(body)["data"]], ["safe-model"]
        )

        self.gateway.configure(
            allowed_models=self.MODELS, sensitive_markers=("EchoGate",)
        )
        for case in ("percent-leak-json", "percent-leak-sse"):
            status, _headers, body = self._request(
                "POST",
                "/v1/responses",
                {
                    "model": "allowed-model",
                    "stream": case.endswith("sse"),
                    "test_case": case,
                },
            )
            self.assertEqual(status, 200)
            self.assertIn(b"%", body)

    def test_unicode_idna_and_ip_canonical_forms_are_private(self):
        before = len(self.relay_server.requests)
        unicode_name = "Caf\N{LATIN SMALL LETTER E WITH ACUTE}AI"
        decomposed = unicodedata.normalize("NFD", unicode_name)
        for leaked, unsolicited_case in (
            (decomposed, "unicode-leak"),
            (
                base64.b64encode(decomposed.encode()).decode().rstrip("="),
                "unicode-b64-leak",
            ),
        ):
            with self.subTest(kind="unicode", leaked=leaked):
                self.gateway.configure(sensitive_markers=(unicode_name,))
                status, _headers, body = self._request(
                    "POST",
                    "/v1/responses",
                    {
                        "model": "allowed-model",
                        "value": leaked,
                        "test_case": "echo-value",
                    },
                )
                self.assertEqual(status, 200)
                self.assertEqual(json.loads(body)["output"], leaked)
                status, _headers, body = self._request(
                    "POST",
                    "/v1/responses",
                    {
                        "model": "allowed-model",
                        "test_case": unsolicited_case,
                    },
                )
                self.assertEqual(status, 502)
                self.assertNotIn(leaked.encode(), body)

        idn = "caf\N{LATIN SMALL LETTER E WITH ACUTE}.example"
        punycode = idn.encode("idna").decode("ascii")
        idn_markers = relay_http._hostname_privacy_markers(idn)
        self.assertIn(punycode, idn_markers)
        self.assertIn(idn, relay_http._hostname_privacy_markers(punycode))
        self.assertIn("ibm", relay_http._hostname_privacy_markers("api.ibm.com"))

        compressed = "2001:db8::1"
        expanded = "2001:0db8:0000:0000:0000:0000:0000:0001"
        ip_markers = relay_http._hostname_privacy_markers(compressed)
        self.assertIn(expanded, ip_markers)
        for scoped in ("fe80::1%eth0", "fe80::1%25eth0"):
            with self.subTest(scoped=scoped):
                scoped_markers = relay_http._hostname_privacy_markers(scoped)
                self.assertIn(
                    "fe80:0000:0000:0000:0000:0000:0000:0001",
                    scoped_markers,
                )
        for markers, leaked, unsolicited_case in (
            (idn_markers, punycode, "idn-leak"),
            (ip_markers, expanded, "ip-leak"),
        ):
            with self.subTest(leaked=leaked):
                self.gateway.configure(sensitive_markers=markers)
                status, _headers, body = self._request(
                    "POST",
                    "/v1/responses",
                    {
                        "model": "allowed-model",
                        "value": leaked,
                        "test_case": "echo-value",
                    },
                )
                self.assertEqual(status, 200)
                self.assertEqual(json.loads(body)["output"], leaked)
                status, _headers, body = self._request(
                    "POST",
                    "/v1/responses",
                    {
                        "model": "allowed-model",
                        "test_case": unsolicited_case,
                    },
                )
                self.assertEqual(status, 502)
                self.assertNotIn(leaked.encode(), body)
        self.assertEqual(len(self.relay_server.requests), before + 8)

    def test_request_identity_is_not_an_oracle_but_tokens_stay_private(self):
        before = len(self.relay_server.requests)
        self.gateway.configure(sensitive_markers=("EchoGate",))
        cases = (
            (
                {"model": "allowed-model"},
                {"Anthropic-Beta": "Echo", "OpenAI-Beta": "Gate"},
                200,
            ),
            (
                {"model": "allowed-model", "parts": ["Echo ", "Gate"]},
                {},
                200,
            ),
            (
                {"model": "allowed-model", "value": "Echo-Gate"},
                {},
                200,
            ),
            (
                {"model": "allowed-model", "value": "OtherGate"},
                {},
                200,
            ),
        )
        responses = []
        for body, headers, expected in cases:
            with self.subTest(body=body, headers=headers):
                status, _response_headers, response_body = self._request(
                    "POST", "/v1/responses", body, headers=headers
                )
                self.assertEqual(status, expected)
                responses.append(response_body)
        self.assertEqual(len(set(responses)), 1)

        midpoint = len(self.TOKEN) // 2
        self.gateway.configure(sensitive_markers=self.MARKERS)
        status, _headers, _body = self._request(
            "POST",
            "/v1/responses",
            {"piece": self.TOKEN[midpoint:], "model": "allowed-model"},
            headers={"Anthropic-Beta": self.TOKEN[:midpoint]},
        )
        self.assertEqual(status, 403)

        for raw in (
            b'{"model":"allowed-model","value":NaN}',
            b'{"model":"allowed-model","value":'
            + (b"9" * 5000)
            + b"}",
        ):
            status, _headers, _body = self._request(
                "POST",
                "/v1/responses",
                raw,
                headers={"Content-Type": "application/json"},
            )
            self.assertEqual(status, 400)
        self.assertEqual(len(self.relay_server.requests), before + len(cases))

    def test_client_origin_literals_do_not_expose_marker_membership(self):
        self.gateway.configure(sensitive_markers=("EchoGate",))
        observations = []
        for case in ("echo-value", "echo-twice"):
            for value in ("EchoGate", "OtherGate"):
                with self.subTest(case=case, value=value):
                    status, headers, body = self._request(
                        "POST",
                        "/v1/responses",
                        {
                            "model": "allowed-model",
                            "test_case": case,
                            "value": value,
                        },
                    )
                    result = json.loads(body)
                    self.assertEqual(status, 200)
                    self.assertEqual(
                        result["output"],
                        value if case == "echo-value" else [value, value],
                    )
                    observations.append(
                        (
                            status,
                            tuple(name.casefold() for name, _value in headers),
                            tuple(sorted(result)),
                        )
                    )
        self.assertEqual(len(set(observations)), 1)

        for value, expected in (
            ("%45choGate", "EchoGate"),
            ("%4ftherGate", "OtherGate"),
        ):
            with self.subTest(encoded=value):
                status, _headers, body = self._request(
                    "POST",
                    "/v1/responses",
                    {
                        "model": "allowed-model",
                        "test_case": "echo-decoded",
                        "value": value,
                    },
                )
                self.assertEqual(status, 200)
                self.assertEqual(json.loads(body)["output"], expected)

        split_marker = "SentinelProvider-DoNotExpose"
        self.gateway.configure(sensitive_markers=(split_marker,))
        for case, parts in (
            ("split-leak-json", ["SentinelProvider-", "DoNotExpose"]),
            ("typed-split-json", ["SentinelProvider-", "DoNotExpose"]),
        ):
            with self.subTest(case=case):
                status, _headers, body = self._request(
                    "POST",
                    "/v1/responses",
                    {
                        "model": "allowed-model",
                        "test_case": case,
                        "parts": parts,
                    },
                )
                self.assertEqual(status, 200)
                self.assertIn(parts[0].encode(), body)
                self.assertIn(b"DoNotExpose", body)

        status, _headers, body = self._request(
            "POST",
            "/v1/responses",
            {"model": "allowed-model", "test_case": "leak-json"},
        )
        self.assertEqual(
            (status, json.loads(body)),
            (502, {"error": "Upstream response rejected"}),
        )
        self.assertNotIn(split_marker.encode(), body)

    def test_inflight_response_keeps_removed_markers_before_commit(self):
        received = threading.Event()
        release = threading.Event()

        class DelayedResponseHandler(BaseHTTPRequestHandler):
            def do_POST(handler):
                length = int(handler.headers.get("Content-Length", "0"))
                handler.rfile.read(length)
                received.set()
                release.wait(timeout=2)
                body = json.dumps(
                    {
                        "model": "allowed-model",
                        "output": ["late-", "secret"],
                    },
                    separators=(",", ":"),
                ).encode()
                handler.send_response(200)
                handler.send_header("Content-Type", "application/json")
                handler.send_header("Content-Length", str(len(body)))
                handler.send_header("Connection", "close")
                handler.end_headers()
                handler.wfile.write(body)

            def log_message(handler, _format, *_args):
                pass

        relay_server = start_server(DelayedResponseHandler)
        gateway = TunnelGateway(
            relay_server.server_address,
            self.TOKEN,
            ("allowed-model",),
            sensitive_markers=("late-secret",),
            secret_markers=("late-secret",),
        )
        gateway_thread = threading.Thread(target=gateway.serve_forever, daemon=True)
        gateway_thread.start()
        result = []
        request_thread = threading.Thread(
            target=lambda: result.append(
                self._call(
                    gateway.server_address,
                    "POST",
                    "/v1/responses",
                    {"model": "allowed-model"},
                )
            ),
            daemon=True,
        )
        try:
            request_thread.start()
            self.assertTrue(received.wait(timeout=1))
            gateway.configure(
                sensitive_markers=("replacement-secret",),
                secret_markers=("replacement-secret",),
            )
            release.set()
            request_thread.join(timeout=2)
            self.assertFalse(request_thread.is_alive())
            status, headers, body = result[0]
            self.assertEqual(
                (status, json.loads(body)),
                (502, {"error": "Upstream response rejected"}),
            )
            self.assertNotIn(b"late", body)
            self.assertNoSecrets(headers, body)
        finally:
            release.set()
            gateway.shutdown()
            gateway.server_close()
            gateway_thread.join(timeout=1)
            relay_server.shutdown()
            relay_server.server_close()

    def test_final_sanitize_and_commit_are_atomic_against_configure(self):
        entered = threading.Event()
        release = threading.Event()
        configured = threading.Event()
        real_sanitize = relay_tunnel._sanitize_json

        def delayed_sanitize(*args, **kwargs):
            value = real_sanitize(*args, **kwargs)
            entered.set()
            release.wait(timeout=2)
            return value

        result = []
        configure_thread = threading.Thread(
            target=lambda: (
                self.gateway.configure(sensitive_markers=("post-commit",)),
                configured.set(),
            ),
            daemon=True,
        )
        request_thread = threading.Thread(
            target=lambda: result.append(
                self._request(
                    "POST",
                    "/v1/responses",
                    {"model": "allowed-model", "test_case": "safe-json"},
                )
            ),
            daemon=True,
        )
        try:
            with patch("relay_tunnel._sanitize_json", side_effect=delayed_sanitize):
                request_thread.start()
                self.assertTrue(entered.wait(timeout=1))
                configure_thread.start()
                self.assertFalse(configured.wait(timeout=0.1))
                release.set()
                request_thread.join(timeout=2)
                configure_thread.join(timeout=2)
            self.assertFalse(request_thread.is_alive())
            self.assertFalse(configure_thread.is_alive())
            self.assertEqual(result[0][0], 200)
            self.assertTrue(configured.is_set())
        finally:
            release.set()

    def test_slow_client_body_write_does_not_hold_configuration_lock(self):
        entered = threading.Event()
        release = threading.Event()
        configured = threading.Event()
        real_commit = relay_tunnel.TunnelHandler._commit

        def delayed_commit(handler, status, content_type, length):
            if length > 1024 * 1024:
                entered.set()
                release.wait(timeout=2)
            return real_commit(handler, status, content_type, length)

        payload = json.dumps(
            {"model": "allowed-model", "test_case": "safe-json"}
        ).encode()
        large_body = b'{"output":"' + (b"x" * (8 * 1024 * 1024)) + b'"}'
        client = socket.create_connection(self.gateway.server_address, timeout=2)
        client.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 1024)
        request = (
            "POST /v1/responses HTTP/1.1\r\n"
            f"Host: 127.0.0.1:{self.gateway.port}\r\n"
            f"Authorization: Bearer {self.TOKEN}\r\n"
            f"{relay_tunnel.CLIENT_IP_HEADER}: 198.51.100.40\r\n"
            "Content-Type: application/json\r\n"
            f"Content-Length: {len(payload)}\r\n"
            "Connection: close\r\n\r\n"
        ).encode() + payload
        configure_thread = threading.Thread(
            target=lambda: (
                self.gateway.configure(sensitive_markers=("post-commit-marker",)),
                configured.set(),
            ),
            daemon=True,
        )
        try:
            with (
                patch.object(
                    relay_tunnel.TunnelHandler,
                    "_buffer_json",
                    return_value=large_body,
                ),
                patch.object(
                    relay_tunnel.TunnelHandler,
                    "_commit",
                    delayed_commit,
                ),
            ):
                client.sendall(request)
                self.assertTrue(entered.wait(timeout=3))
                configure_thread.start()
                self.assertTrue(
                    configured.wait(timeout=0.5),
                    "Slow reader held the shared tunnel policy lock",
                )
                release.set()
        finally:
            release.set()
            client.close()
            if configure_thread.ident is not None:
                configure_thread.join(timeout=1)

    def test_active_passthrough_is_blocked_before_upstream(self):
        proxy = start_proxy(f"http://127.0.0.1:{self.relay_server.server_port}")
        gateway = TunnelGateway(
            proxy.server_address,
            self.TOKEN,
            ("allowed-model",),
            route_guard=proxy._tunnel_route_safe,
        )
        gateway_thread = threading.Thread(target=gateway.serve_forever, daemon=True)
        gateway_thread.start()
        try:
            sensitive = set(proxy._tunnel_sensitive_markers())
            secrets = set(proxy._tunnel_secret_markers())
            self.assertIn("echo", sensitive)
            self.assertTrue(secrets)
            self.assertNotIn("echo", secrets)
            self.assertTrue(secrets.issubset(sensitive))
            self.assertFalse(proxy._tunnel_route_safe())
            status, _headers, body = self._call(
                gateway.server_address,
                "POST",
                "/v1/responses",
                {"model": "allowed-model"},
            )
            self.assertEqual((status, json.loads(body)), (403, {"error": "Request rejected"}))
            status, _headers, _body = self._call(
                proxy.server_address,
                "POST",
                "/v1/responses",
                {"model": "allowed-model"},
                token=None,
                headers={"X-Provider-Switch-Tunnel": "1"},
            )
            self.assertEqual(status, 503)
            self.assertEqual(self.relay_server.requests, [])
        finally:
            gateway.shutdown()
            gateway.server_close()
            gateway_thread.join(timeout=1)
            proxy.shutdown()
            proxy.server_close()

    def test_client_ip_header_and_rate_gate_capacity_fail_closed(self):
        gateway = TunnelGateway(
            self.relay_server.server_address,
            self.TOKEN,
            ("allowed-model",),
            public_rpm=6000,
        )
        gateway_thread = threading.Thread(target=gateway.serve_forever, daemon=True)
        gateway_thread.start()
        body = json.dumps({"model": "allowed-model", "test_case": "safe-json"}).encode()
        try:
            for value in ("", "not-an-ip", "198.51.100.1, 203.0.113.1"):
                status, _headers, result = self._call(
                    gateway.server_address,
                    "POST",
                    "/v1/responses",
                    body,
                    headers={
                        "Content-Type": "application/json",
                        relay_tunnel.CLIENT_IP_HEADER: value,
                    },
                )
                self.assertEqual((status, json.loads(result)), (400, {"error": "Invalid request"}))

            connection = http.client.HTTPConnection(*gateway.server_address, timeout=2)
            connection.putrequest("POST", "/v1/responses")
            connection.putheader("Authorization", f"Bearer {self.TOKEN}")
            connection.putheader("Content-Type", "application/json")
            connection.putheader("Content-Length", str(len(body)))
            connection.putheader(relay_tunnel.CLIENT_IP_HEADER, "198.51.100.1")
            connection.putheader(relay_tunnel.CLIENT_IP_HEADER, "198.51.100.2")
            connection.endheaders(body)
            response = connection.getresponse()
            self.assertEqual(response.status, 400)
            response.read()
            connection.close()

            with patch("relay_tunnel.MAX_RATE_CLIENTS", 1):
                first = self._call(
                    gateway.server_address,
                    "POST",
                    "/v1/responses",
                    body,
                    headers={
                        "Content-Type": "application/json",
                        relay_tunnel.CLIENT_IP_HEADER: "198.51.100.10",
                    },
                )
                second = self._call(
                    gateway.server_address,
                    "POST",
                    "/v1/responses",
                    body,
                    headers={
                        "Content-Type": "application/json",
                        relay_tunnel.CLIENT_IP_HEADER: "198.51.100.11",
                    },
                )
                self.assertEqual(first[0], 200)
                self.assertEqual(
                    (second[0], json.loads(second[2])),
                    (503, {"error": "Request unavailable"}),
                )
                self.assertNotEqual(second[0], 429)

                with patch("relay_tunnel.RATE_CLIENT_IDLE_SECONDS", 0):
                    recovered = self._call(
                        gateway.server_address,
                        "POST",
                        "/v1/responses",
                        body,
                        headers={
                            "Content-Type": "application/json",
                            relay_tunnel.CLIENT_IP_HEADER: "198.51.100.11",
                        },
                    )
                self.assertEqual(recovered[0], 200)
        finally:
            gateway.shutdown()
            gateway.server_close()
            gateway_thread.join(timeout=1)

    def test_per_ip_fifo_survives_rpm_change_and_drops_disconnected_waiter(self):
        gateway = TunnelGateway(
            self.relay_server.server_address,
            self.TOKEN,
            ("allowed-model",),
            public_rpm=60,
        )
        gateway_thread = threading.Thread(target=gateway.serve_forever, daemon=True)
        gateway_thread.start()
        address = gateway.server_address

        def call(sequence, client_ip):
            return self._call(
                address,
                "POST",
                "/v1/responses",
                {
                    "model": "allowed-model",
                    "test_case": "safe-json",
                    "sequence": sequence,
                },
                headers={relay_tunnel.CLIENT_IP_HEADER: client_ip},
            )

        def wait_queued(expected):
            deadline = time.monotonic() + 2
            while time.monotonic() < deadline:
                if gateway.rate_snapshot()["queued"] == expected:
                    return
                time.sleep(0.01)
            self.fail(f"Expected {expected} queued requests")

        try:
            self.relay_server.requests.clear()
            self.assertEqual(call(1, "198.51.100.20")[0], 200)
            results = {}
            second = threading.Thread(
                target=lambda: results.setdefault(2, call(2, "198.51.100.20")),
                daemon=True,
            )
            third = threading.Thread(
                target=lambda: results.setdefault(3, call(3, "::ffff:198.51.100.20")),
                daemon=True,
            )
            second.start()
            wait_queued(1)
            third.start()
            wait_queued(2)
            gateway.configure(public_rpm=600)
            second.join(timeout=1)
            third.join(timeout=1)
            self.assertFalse(second.is_alive())
            self.assertFalse(third.is_alive())
            self.assertEqual([results[2][0], results[3][0]], [200, 200])
            self.assertNotIn(429, (results[2][0], results[3][0]))
            self.assertEqual(
                [json.loads(item["body"])["sequence"] for item in self.relay_server.requests[-3:]],
                [1, 2, 3],
            )
            self.assertEqual(gateway.rate_snapshot(), {"rpm_per_ip": 600, "queued": 0})

            gateway.configure(public_rpm=1)
            self.assertEqual(call(4, "198.51.100.30")[0], 200)
            before = len(self.relay_server.requests)
            body_read = threading.Event()
            real_body = relay_tunnel.TunnelHandler._body

            def observed_body(handler, length):
                body_read.set()
                return real_body(handler, length)

            with patch.object(
                relay_tunnel.TunnelHandler, "_body", observed_body
            ):
                client = socket.create_connection(address, timeout=2)
                client.sendall(
                    (
                        "POST /v1/responses HTTP/1.1\r\n"
                        f"Host: {address[0]}:{address[1]}\r\n"
                        f"Authorization: Bearer {self.TOKEN}\r\n"
                        "Content-Type: application/json\r\n"
                        f"Content-Length: {relay_tunnel.MAX_BODY_BYTES}\r\n"
                        f"{relay_tunnel.CLIENT_IP_HEADER}: 198.51.100.30\r\n"
                        "Connection: close\r\n\r\n"
                    ).encode()
                )
                wait_queued(1)
                self.assertFalse(body_read.wait(timeout=0.2))
                client.close()
                wait_queued(0)
                self.assertFalse(body_read.is_set())
            self.assertEqual(len(self.relay_server.requests), before)
        finally:
            gateway.shutdown()
            gateway.server_close()
            gateway_thread.join(timeout=1)

    def test_publisher_profile_parser_is_strict(self):
        port, slug = relay_tunnel.parse_publisher_profile(self.PROFILE)
        self.assertEqual(port, 20000)
        self.assertEqual(
            self.PUBLIC_URL,
            f"https://luxuryprivate.duckdns.org/model-tunnel/{slug}/v1",
        )
        for value in (
            "",
            "v2.20000." + "a" * 48,
            "v1.19999." + "a" * 48,
            "v1.30000." + "a" * 48,
            "v1.20000." + "A" * 48,
            "v1.20000." + "a" * 47,
            " v1.20000." + "a" * 48,
        ):
            with self.subTest(value=value):
                with self.assertRaises(ValueError):
                    relay_tunnel.parse_publisher_profile(value)

    def test_pinned_known_hosts_is_created_and_repaired_atomically(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            identity = Path(temporary_directory) / "model-tunnel_ed25519"
            identity.touch()
            expected = (
                f"{relay_tunnel.SSH_HOST} {relay_tunnel.SSH_HOST_KEY}\n"
            )

            known_hosts = Path(relay_tunnel._ensure_ssh_known_hosts(str(identity)))
            self.assertEqual(known_hosts.name, relay_tunnel.SSH_KNOWN_HOSTS_NAME)
            self.assertEqual(known_hosts.read_text(encoding="ascii"), expected)

            known_hosts.write_text(
                "81.90.28.122 ssh-ed25519 attacker-key\n", encoding="ascii"
            )
            repaired = relay_tunnel._ensure_ssh_known_hosts(str(identity))
            self.assertEqual(Path(repaired).read_text(encoding="ascii"), expected)
            self.assertEqual(
                list(known_hosts.parent.glob(f".{known_hosts.name}.*.tmp")), []
            )

    def test_https_models_readiness_is_authenticated_and_exact(self):
        token = self.TOKEN
        readiness_token = "readiness-000000000000000000000000000000000000000"
        body = json.dumps(
            {
                "object": "list",
                "data": [{"id": "allowed-model", "object": "model"}],
            },
            separators=(",", ":"),
        ).encode()
        response = Mock(status=200)
        response.headers.get_all.return_value = [str(len(body))]
        response.getheader.side_effect = lambda name: {
            "Content-Encoding": None,
            "Content-Type": "application/json",
        }.get(name)
        response.read.return_value = body
        connection = Mock()
        connection.getresponse.return_value = response

        with patch(
            "relay_tunnel.http.client.HTTPSConnection", return_value=connection
        ) as https:
            self.assertTrue(
                relay_tunnel._https_models_ready(
                    self.PUBLIC_URL,
                    token,
                    readiness_token,
                    frozenset(("allowed-model",)),
                    1.5,
                )
            )
        https.assert_called_once_with(
            "luxuryprivate.duckdns.org", 443, timeout=1.5
        )
        method, target = connection.request.call_args.args[:2]
        headers = connection.request.call_args.kwargs["headers"]
        slug = self.PROFILE.rsplit(".", 1)[1]
        self.assertEqual(
            (method, target),
            ("GET", f"/model-tunnel/{slug}/v1/models"),
        )
        self.assertEqual(headers["Authorization"], f"Bearer {token}")
        self.assertEqual(headers[relay_tunnel.READINESS_HEADER], readiness_token)

    def test_ssh_reverse_tunnel_reconciles_policy_before_fixed_url_is_online(self):
        process = FakeSshProcess("")
        executable = r"C:\Windows\System32\OpenSSH\ssh.exe"
        new_token = "new-share-token-000000000000000000000000000000000000"
        alternate_profile = "v1.23456." + ("b" * 48)
        controller = TunnelController(
            self.relay_server.server_address,
            self.TOKEN,
            ("allowed-model",),
            startup_timeout=2,
            readiness_delay=0,
        )
        probes = []

        def probe(url, token, readiness_token, models, _timeout):
            command = popen.call_args.args[0]
            forwarding = command[command.index("-R") + 1]
            gateway_port = int(forwarding.rsplit(":", 1)[1])
            status, _headers, _body = self._call(
                ("127.0.0.1", gateway_port),
                "GET",
                "/v1/models",
                token=token,
            )
            self.assertEqual(status, 403)
            status, _headers, body = self._call(
                ("127.0.0.1", gateway_port),
                "GET",
                "/v1/models",
                token=token,
                headers={relay_tunnel.READINESS_HEADER: readiness_token},
            )
            self.assertEqual(status, 200)
            self.assertEqual(
                {item["id"] for item in json.loads(body)["data"]}, set(models)
            )
            probes.append((url, token, readiness_token, models))
            if len(probes) == 1:
                with self.assertRaisesRegex(RuntimeError, "Stop the tunnel"):
                    controller.configure(publisher_profile=alternate_profile)
                controller.configure(
                    token=new_token,
                    allowed_models=("fresh-model",),
                    sensitive_markers=("fresh-private",),
                )
            return True

        try:
            with (
                patch("relay_tunnel._find_ssh", return_value=executable),
                patch("relay_tunnel._find_ssh_identity", return_value=self.IDENTITY),
                patch(
                    "relay_tunnel._ensure_ssh_known_hosts",
                    return_value=self.KNOWN_HOSTS,
                ),
                patch("relay_tunnel.subprocess.Popen", return_value=process) as popen,
                patch("relay_tunnel._https_models_ready", side_effect=probe),
            ):
                started = controller.start()
            self.assertEqual(
                started,
                {
                    "state": "running",
                    "url": self.PUBLIC_URL,
                    "allowed_count": 1,
                    "error": "",
                    "rpm_per_ip": 0,
                    "queued": 0,
                },
            )
            self.assertEqual([item[1] for item in probes], [self.TOKEN, new_token])
            self.assertEqual([item[3] for item in probes], [
                frozenset(("allowed-model",)),
                frozenset(("fresh-model",)),
            ])

            command = popen.call_args.args[0]
            options = popen.call_args.kwargs
            self.assertEqual(command[0], executable)
            self.assertEqual(command.count("-N"), 1)
            self.assertEqual(command.count("-T"), 1)
            self.assertEqual(command[command.index("-F") + 1], "NUL")
            self.assertEqual(command.count("-i"), 1)
            self.assertEqual(command[command.index("-i") + 1], self.IDENTITY)
            self.assertIn("StrictHostKeyChecking=yes", command)
            self.assertIn(f"UserKnownHostsFile={self.KNOWN_HOSTS}", command)
            self.assertIn("GlobalKnownHostsFile=NUL", command)
            self.assertIn("HostKeyAlgorithms=ssh-ed25519", command)
            self.assertIn("IdentityAgent=none", command)
            self.assertIn("PasswordAuthentication=no", command)
            self.assertIn("KbdInteractiveAuthentication=no", command)
            self.assertEqual(command[-1], relay_tunnel.SSH_DESTINATION)
            self.assertRegex(
                command[command.index("-R") + 1],
                r"^127\.0\.0\.1:20000:127\.0\.0\.1:\d+$",
            )
            command_text = " ".join(command)
            for secret in (
                self.TOKEN,
                new_token,
                "allowed-model",
                "fresh-model",
                "fresh-private",
                *(item[2] for item in probes),
            ):
                self.assertNotIn(secret, command_text)
            self.assertIs(options["stdout"], subprocess.DEVNULL)
            self.assertIs(options["stderr"], subprocess.DEVNULL)
            self.assertFalse(options["shell"])
            self.assertNotIn("FREEMODEL_API_KEY", options["env"])
            self.assertNotIn("SSH_AUTH_SOCK", options["env"])
            self.assertEqual(controller._gateway._readiness_token, "")
            with self.assertRaisesRegex(RuntimeError, "Stop the tunnel"):
                controller.configure(publisher_profile=alternate_profile)

            status, _headers, _body = self._call(
                controller._gateway.server_address,
                "GET",
                "/v1/models",
                token=self.TOKEN,
            )
            self.assertEqual(status, 401)
            status, _headers, body = self._call(
                controller._gateway.server_address,
                "GET",
                "/v1/models",
                token=new_token,
            )
            self.assertEqual(status, 200)
            self.assertEqual(json.loads(body)["data"][0]["id"], "fresh-model")
        finally:
            controller.stop()
        controller.configure(publisher_profile=alternate_profile)
        self.assertEqual(
            relay_tunnel.publisher_url(controller._publisher_profile),
            "https://luxuryprivate.duckdns.org/model-tunnel/"
            + ("b" * 48)
            + "/v1",
        )

    def test_ssh_probe_failure_rolls_back_gateway_and_process(self):
        process = FakeSshProcess("")
        controller = TunnelController(
            self.relay_server.server_address,
            self.TOKEN,
            ("allowed-model",),
            startup_timeout=1,
            readiness_delay=0,
        )

        def fail_probe(*_args):
            process.terminate()
            return False

        try:
            with (
                patch("relay_tunnel._find_ssh", return_value="ssh.exe"),
                patch("relay_tunnel._find_ssh_identity", return_value=self.IDENTITY),
                patch(
                    "relay_tunnel._ensure_ssh_known_hosts",
                    return_value=self.KNOWN_HOSTS,
                ),
                patch("relay_tunnel.subprocess.Popen", return_value=process) as popen,
                patch("relay_tunnel._https_models_ready", side_effect=fail_probe),
            ):
                with self.assertRaisesRegex(RuntimeError, "could not start"):
                    controller.start()
            self.assertEqual(
                controller.snapshot(),
                {
                    "state": "error",
                    "url": "",
                    "allowed_count": 1,
                    "error": "Tunnel could not start",
                    "rpm_per_ip": 0,
                    "queued": 0,
                },
            )
            forwarding = popen.call_args.args[0][
                popen.call_args.args[0].index("-R") + 1
            ]
            gateway_port = int(forwarding.rsplit(":", 1)[1])
            with self.assertRaises(OSError):
                socket.create_connection(("127.0.0.1", gateway_port), timeout=0.2)
            self.assertTrue(process.terminated)
        finally:
            controller.stop()

    def test_ssh_readiness_allows_slow_tls_within_startup_deadline(self):
        self.assertEqual(relay_tunnel.READINESS_INITIAL_DELAY, 0)
        process = FakeSshProcess("")
        controller = TunnelController(
            self.relay_server.server_address,
            self.TOKEN,
            ("allowed-model",),
            startup_timeout=10,
            readiness_delay=0,
        )
        probe_timeouts = []

        def slow_probe(_url, _token, _readiness, _models, timeout):
            probe_timeouts.append(timeout)
            return timeout >= 4

        try:
            with (
                patch("relay_tunnel._find_ssh", return_value="ssh.exe"),
                patch("relay_tunnel._find_ssh_identity", return_value=self.IDENTITY),
                patch(
                    "relay_tunnel._ensure_ssh_known_hosts",
                    return_value=self.KNOWN_HOSTS,
                ),
                patch("relay_tunnel.subprocess.Popen", return_value=process),
                patch("relay_tunnel._https_models_ready", side_effect=slow_probe),
            ):
                started = controller.start()
            self.assertEqual(started["state"], "running")
            self.assertGreaterEqual(probe_timeouts[0], 4)
            self.assertLessEqual(
                probe_timeouts[0], relay_tunnel.READINESS_PROBE_TIMEOUT
            )
        finally:
            controller.stop()

    def test_relay_start_does_not_hold_policy_lock_during_readiness(self):
        process = FakeSshProcess("")
        server = relay.RelayServer(
            ("127.0.0.1", 0),
            relay.RelayHandler,
            echo_api_key="",
            config=False,
            history=False,
        )
        server.select("echo")
        server.set_tunnel_allowed_models(("allowed-model",))
        server.tunnel._startup_timeout = 1
        server.tunnel._readiness_delay = 0

        def readiness_from_gateway_thread(*_args):
            acquired = []

            def enter_policy():
                if server._config_lock.acquire(timeout=0.25):
                    acquired.append(True)
                    server._config_lock.release()

            worker = threading.Thread(target=enter_policy)
            worker.start()
            worker.join(timeout=0.5)
            return bool(acquired)

        try:
            with (
                patch("relay_tunnel._find_ssh", return_value="ssh.exe"),
                patch("relay_tunnel._find_ssh_identity", return_value=self.IDENTITY),
                patch(
                    "relay_tunnel._ensure_ssh_known_hosts",
                    return_value=self.KNOWN_HOSTS,
                ),
                patch("relay_tunnel.subprocess.Popen", return_value=process),
                patch(
                    "relay_tunnel._https_models_ready",
                    side_effect=readiness_from_gateway_thread,
                ),
            ):
                self.assertEqual(server.start_tunnel()["state"], "running")
        finally:
            server.server_close()

    def test_ssh_start_retries_transient_remote_bind_rejection(self):
        rejected = RejectedSshProcess()
        process = FakeSshProcess("")
        controller = TunnelController(
            self.relay_server.server_address,
            self.TOKEN,
            ("allowed-model",),
            startup_timeout=2,
            readiness_delay=0,
        )
        try:
            with (
                patch("relay_tunnel._find_ssh", return_value="ssh.exe"),
                patch("relay_tunnel._find_ssh_identity", return_value=self.IDENTITY),
                patch(
                    "relay_tunnel._ensure_ssh_known_hosts",
                    return_value=self.KNOWN_HOSTS,
                ),
                patch(
                    "relay_tunnel.subprocess.Popen",
                    side_effect=(rejected, process),
                ) as popen,
                patch("relay_tunnel._https_models_ready", return_value=True),
            ):
                started = controller.start()
            self.assertEqual(started["state"], "running")
            self.assertEqual(popen.call_count, 2)
        finally:
            controller.stop()

    def test_removed_marker_survives_controller_stop_and_restart(self):
        first = FakeSshProcess("")
        second = FakeSshProcess("")
        controller = TunnelController(
            self.relay_server.server_address,
            self.TOKEN,
            ("allowed-model",),
            sensitive_markers=("retired-secret",),
            secret_markers=("retired-secret",),
            startup_timeout=1,
            readiness_delay=0,
        )
        try:
            with (
                patch("relay_tunnel._find_ssh", return_value="ssh.exe"),
                patch("relay_tunnel._find_ssh_identity", return_value=self.IDENTITY),
                patch(
                    "relay_tunnel._ensure_ssh_known_hosts",
                    return_value=self.KNOWN_HOSTS,
                ),
                patch("relay_tunnel.subprocess.Popen", side_effect=(first, second)),
                patch("relay_tunnel._https_models_ready", return_value=True),
            ):
                controller.start()
                controller.configure(
                    sensitive_markers=("replacement-secret",),
                    secret_markers=("replacement-secret",),
                )
                controller.stop()
                controller.start()

                status, headers, body = self._call(
                    controller._gateway.server_address,
                    "POST",
                    "/v1/responses",
                    {
                        "model": "allowed-model",
                        "test_case": "retired-marker",
                    },
                )
                self.assertEqual(
                    (status, json.loads(body)),
                    (502, {"error": "Upstream response rejected"}),
                )
                self.assertNotIn(b"retired-secret", body)
                self.assertNoSecrets(headers, body)
        finally:
            controller.stop()

    def test_ssh_argv_restart_rollback_and_idempotent_stop(self):
        failed_process = FakeSshProcess("")
        process = StubbornSshProcess("")
        executable = r"C:\Windows\System32\OpenSSH\ssh.exe"
        controller = TunnelController(
            self.relay_server.server_address,
            self.TOKEN,
            ("allowed-model",),
            sensitive_markers=self.MARKERS,
            startup_timeout=1,
            readiness_delay=0,
        )

        def quick(operation):
            result = []
            errors = []

            def invoke():
                try:
                    result.append(operation())
                except BaseException as error:
                    errors.append(error)

            thread = threading.Thread(target=invoke, daemon=True)
            thread.start()
            thread.join(timeout=1)
            self.assertFalse(thread.is_alive(), "Tunnel lifecycle operation deadlocked")
            if errors:
                raise errors[0]
            return result[0]

        def assert_gateway_closed(call):
            command = call.args[0]
            port = int(command[command.index("-R") + 1].rsplit(":", 1)[1])
            with self.assertRaises(OSError):
                socket.create_connection(("127.0.0.1", port), timeout=0.2)

        probe_count = 0

        def probe(*_args):
            nonlocal probe_count
            probe_count += 1
            if probe_count == 1:
                failed_process.terminate()
                return False
            return True

        self.assertEqual(quick(controller.stop)["state"], "stopped")
        with (
            patch("relay_tunnel._find_ssh", return_value=executable),
            patch("relay_tunnel._find_ssh_identity", return_value=self.IDENTITY),
            patch(
                "relay_tunnel._ensure_ssh_known_hosts",
                return_value=self.KNOWN_HOSTS,
            ),
            patch(
                "relay_tunnel.subprocess.Popen",
                side_effect=(
                    OSError("simulated spawn failure"),
                    failed_process,
                    process,
                ),
            ) as popen,
            patch("relay_tunnel._https_models_ready", side_effect=probe),
        ):
            with self.assertRaisesRegex(RuntimeError, "could not start"):
                controller.start()
            failed = controller.snapshot()
            self.assertEqual(failed["state"], "error")
            self.assertEqual(failed["url"], "")
            assert_gateway_closed(popen.call_args_list[0])
            self.assertEqual(quick(controller.stop)["state"], "stopped")

            with self.assertRaisesRegex(RuntimeError, "could not start"):
                controller.start()
            assert_gateway_closed(popen.call_args_list[1])
            self.assertTrue(failed_process.terminated)
            self.assertEqual(quick(controller.stop)["state"], "stopped")

            started = controller.start()
            self.assertEqual(
                started,
                {
                    "state": "running",
                    "url": self.PUBLIC_URL,
                    "allowed_count": 1,
                    "error": "",
                    "rpm_per_ip": 0,
                    "queued": 0,
                },
            )
            self.assertEqual(quick(controller.start), started)
            command = popen.call_args_list[-1].args[0]
            options = popen.call_args_list[-1].kwargs
            self.assertEqual(command[0], executable)
            self.assertEqual(command.count("-N"), 1)
            self.assertEqual(command.count("-T"), 1)
            self.assertEqual(command[command.index("-i") + 1], self.IDENTITY)
            self.assertEqual(command[-1], relay_tunnel.SSH_DESTINATION)
            self.assertEqual(
                command[command.index("-R") + 1],
                f"127.0.0.1:20000:127.0.0.1:{controller._gateway.port}",
            )
            command_text = " ".join(command)
            for secret in (*self.MARKERS, self.TOKEN, *self.MODELS):
                self.assertNotIn(secret, command_text)
            self.assertFalse(options.get("shell", False))
            self.assertNotIn("FREEMODEL_API_KEY", options["env"])

            self.assertEqual(quick(controller.stop)["state"], "stopped")
            self.assertEqual(quick(controller.stop)["state"], "stopped")
            self.assertTrue(process.terminated)
            self.assertTrue(process.killed)

    def test_restart_closes_stale_gateway_before_monitor_cleanup(self):
        first = FakeSshProcess("")
        second = FakeSshProcess("")
        controller = TunnelController(
            self.relay_server.server_address,
            self.TOKEN,
            ("allowed-model",),
            startup_timeout=1,
            readiness_delay=0,
        )
        release_monitor = threading.Event()
        real_monitor = controller._monitor

        def delayed_monitor(process):
            if process is first:
                release_monitor.wait(timeout=2)
            real_monitor(process)

        try:
            with (
                patch("relay_tunnel._find_ssh", return_value="ssh.exe"),
                patch("relay_tunnel._find_ssh_identity", return_value=self.IDENTITY),
                patch(
                    "relay_tunnel._ensure_ssh_known_hosts",
                    return_value=self.KNOWN_HOSTS,
                ),
                patch("relay_tunnel.subprocess.Popen", side_effect=(first, second)),
                patch("relay_tunnel._https_models_ready", return_value=True),
                patch.object(controller, "_monitor", side_effect=delayed_monitor),
            ):
                controller.start()
                stale_gateway = controller._gateway
                stale_thread = controller._gateway_thread
                stale_address = stale_gateway.server_address

                first.done.set()
                restarted = controller.start()

                self.assertEqual(
                    restarted["url"],
                    self.PUBLIC_URL,
                )
                self.assertIsNot(controller._gateway, stale_gateway)
                self.assertFalse(stale_thread.is_alive())
                self.assertEqual(stale_gateway.fileno(), -1)
                with self.assertRaises(OSError):
                    socket.create_connection(stale_address, timeout=0.2)

                release_monitor.set()
                deadline = time.monotonic() + 1
                while controller._process is not second and time.monotonic() < deadline:
                    time.sleep(0.01)
                self.assertIs(controller._process, second)
                self.assertEqual(controller.snapshot()["state"], "running")
        finally:
            release_monitor.set()
            controller.stop()


class RelayTest(unittest.TestCase):
    def test_provider_switch_stops_tunnel_and_invalidates_models(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        try:
            proxy.set_tunnel_allowed_models(("gpt-old-provider",))
            with patch.object(proxy.tunnel, "stop", wraps=proxy.tunnel.stop) as stop:
                proxy.select("echo")
                self.assertEqual(proxy.tunnel_allowed_models(), ())
                stop.assert_called_once_with()

                proxy.select("echo")
                stop.assert_called_once_with()

                proxy.set_tunnel_allowed_models(("gpt-echo",))
                proxy.toggle()
                self.assertEqual(proxy.tunnel_allowed_models(), ())
                self.assertEqual(stop.call_count, 2)
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_terminal_usage_survives_client_disconnect(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        original_write = relay.RelayHandler._write_client

        def disconnect_on_terminal(handler, body):
            if b'"response.completed"' in body:
                raise relay._ClientDisconnected
            return original_write(handler, body)

        try:
            with patch.object(
                relay.RelayHandler, "_write_client", disconnect_on_terminal
            ):
                connection = http.client.HTTPConnection(
                    "127.0.0.1", proxy.server_port, timeout=3
                )
                connection.request(
                    "POST",
                    "/v1/stream",
                    b'{"model":"disconnect-test"}',
                    {"Content-Type": "application/json"},
                )
                response = connection.getresponse()
                response.read()
                connection.close()

            deadline = time.monotonic() + 1
            while proxy.metrics.snapshot()["active"] and time.monotonic() < deadline:
                time.sleep(0.01)
            event = proxy.metrics.snapshot()["recent"][-1]
            self.assertEqual(event["state"], "cancelled")
            self.assertEqual(event["output_tokens"], 2)
            self.assertGreater(event["tokens_per_second"], 0)
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_token_speed_includes_model_time_before_headers(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        try:
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=3
            )
            connection.request(
                "POST",
                "/v1/token-speed",
                b'{"model":"speed-test"}',
                {"Content-Type": "application/json"},
            )
            response = connection.getresponse()
            self.assertEqual(response.status, 200)
            response.read()
            connection.close()

            deadline = time.monotonic() + 1
            while proxy.metrics.snapshot()["active"] and time.monotonic() < deadline:
                time.sleep(0.01)
            event = proxy.metrics.snapshot()["recent"][-1]
            self.assertEqual(event["output_tokens"], 24)
            self.assertGreater(event["tokens_per_second"], 0)
            measured_seconds = event["output_tokens"] / event["tokens_per_second"]
            self.assertGreaterEqual(measured_seconds, 0.1)
            self.assertLess(measured_seconds, 1)
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_forward_switch_cache_and_stream(self):
        local = start_server(UpstreamHandler, "Local")
        echo = start_server(UpstreamHandler, "EchoGate")
        proxy = start_proxy(
            f"http://127.0.0.1:{local.server_port}",
            f"http://127.0.0.1:{echo.server_port}/v1",
        )
        self.assertEqual(proxy.request_queue_size, 128)
        request_body = json.dumps(
            {
                "input": [{
                    "type": "message",
                    "content": [
                        {
                            "type": "input_text",
                            "text": "prefix",
                            "cache_control": {"type": "ephemeral", "ttl": "5m"},
                        },
                        {
                            "type": "tool_call",
                            "input": {
                                "cache_control": {"type": "ephemeral", "ttl": "5m"}
                            },
                        },
                    ],
                }],
                "metadata": {
                    "cache_control": {"type": "ephemeral", "ttl": "5m"}
                },
            }
        ).encode()

        def post(path, body=request_body):
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=3
            )
            connection.request(
                "POST",
                path,
                body,
                {
                    "Authorization": "Bearer test",
                    "Content-Type": "application/json",
                    "Connection": "X-Remove",
                    "X-Remove": "must-not-pass",
                },
            )
            return connection, connection.getresponse()

        try:
            proxy.select("local")
            connection, response = post("/v1/responses?q=one")
            local_result = json.loads(response.read())
            self.assertEqual(response.status, 201)
            self.assertEqual(local_result["path"], "/v1/responses?q=one")
            self.assertEqual(local_result["authorization"], "Bearer test")
            self.assertIsNone(local_result["x_api_key"])
            self.assertIsNone(local_result["removed"])
            self.assertEqual(
                local_result["body"]["input"][0]["content"][0]["cache_control"]["ttl"],
                "5m",
            )
            self.assertNotIn("cache_control", local_result["body"])
            self.assertIsNone(response.getheader("X-Private"))
            connection.close()

            proxy.toggle()
            connection, response = post("/responses?q=two")
            echo_result = json.loads(response.read())
            self.assertEqual(echo_result["path"], "/v1/responses?q=two")
            self.assertEqual(echo_result["authorization"], "Bearer test")
            self.assertIsNone(echo_result["x_api_key"])
            self.assertNotIn("cache_control", echo_result["body"])
            self.assertEqual(
                echo_result["body"]["input"][0]["content"][0]["cache_control"]["ttl"],
                "1h",
            )
            self.assertEqual(
                echo_result["body"]["input"][0]["content"][1]["input"]
                ["cache_control"]["ttl"],
                "5m",
            )
            self.assertEqual(
                echo_result["body"]["metadata"]["cache_control"]["ttl"], "5m"
            )
            connection.close()

            connection, response = post("/v1/messages")
            anthropic_result = json.loads(response.read())
            self.assertIsNone(anthropic_result["authorization"])
            self.assertEqual(anthropic_result["x_api_key"], "test")
            self.assertNotIn("cache_control", anthropic_result["body"])
            self.assertEqual(
                anthropic_result["body"]["input"][0]["content"][0]
                ["cache_control"]["ttl"],
                "1h",
            )
            connection.close()

            connection, response = post("/v1/framing", b"{}")
            self.assertIsNone(response.getheader("Content-Length"))
            self.assertEqual(response.read(), b"hello")
            connection.close()

            proxy.select("local")
            total_before_partial_request = proxy.metrics.snapshot()["total"]
            raw_client = socket.create_connection(("127.0.0.1", proxy.server_port))
            try:
                raw_client.sendall(
                    b"POST /v1/responses HTTP/1.1\r\n"
                    b"Host: 127.0.0.1\r\n"
                    b"Content-Type: application/json\r\n"
                    b"Content-Length: 10\r\n\r\n12345"
                )
                deadline = time.monotonic() + 1
                while (
                    proxy.metrics.snapshot()["total"] == total_before_partial_request
                    and time.monotonic() < deadline
                ):
                    time.sleep(0.01)
                self.assertEqual(
                    proxy.metrics.snapshot()["total"], total_before_partial_request + 1
                )
                proxy.toggle()
                raw_client.sendall(b"67890")
                raw_response = http.client.HTTPResponse(raw_client)
                raw_response.begin()
                self.assertEqual(json.loads(raw_response.read())["provider"], "Local")
            finally:
                raw_client.close()

            connection, response = post(
                "/v1/stream", json.dumps({"model": "stream-model"}).encode()
            )
            started = time.monotonic()
            self.assertEqual(response.readline(), b"data: first\n")
            self.assertLess(time.monotonic() - started, 0.5)
            live = proxy.metrics.snapshot()
            self.assertEqual(live["active"], 1)
            self.assertGreaterEqual(live["bytes_out"], len(b"data: first\n\n"))
            self.assertEqual(live["provider_status"]["echo"]["state"], "active")
            self.assertEqual(live["live"][0]["model"], "stream-model")
            self.assertEqual(live["live"][0]["state"], "active")
            self.assertIn(b"data: second", response.read())
            connection.close()

            connection, response = post("/v1/reset", b"{}")
            self.assertEqual(response.status, 201)
            self.assertEqual(json.loads(response.read())["path"], "/v1/reset")
            self.assertEqual(echo.reset_count, 2)
            connection.close()

            connection, response = post("/v1/truncated", b"{}")
            self.assertEqual(response.status, 200)
            self.assertEqual(response.getheader("Content-Length"), "20")
            with self.assertRaises(http.client.IncompleteRead):
                response.read()
            connection.close()

            snapshot = proxy.metrics.snapshot()
            self.assertEqual(snapshot["total"], 8)
            self.assertEqual(snapshot["active"], 0)
            self.assertEqual(snapshot["errors"], 1)
            self.assertEqual(snapshot["cancelled"], 0)
            self.assertTrue(all("?" not in event["path"] for event in snapshot["recent"]))
            self.assertEqual(sum(event["cache_1h"] for event in snapshot["recent"]), 2)

            proxy.toggle()
            self.assertEqual(proxy.provider()[0], "Local")
        finally:
            for server in (proxy, echo, local):
                server.shutdown()
                server.server_close()

    def test_runtime_key_and_all_429_retries(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}",
            f"http://127.0.0.1:{upstream.server_port}/v1",
            echo_api_key="relay-test-key",
            echo_rpm=6000,
        )

        def post(path):
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=3
            )
            connection.request(
                "POST",
                path,
                b"{}",
                {
                    "Authorization": "Bearer client-test-key",
                    "Content-Type": "application/json",
                },
            )
            response = connection.getresponse()
            result = json.loads(response.read())
            status = response.status
            connection.close()
            return status, result

        try:
            proxy.select("echo")
            upstream.retry_count = 0
            status, result = post("/retry")
            self.assertEqual(status, 201)
            self.assertEqual(upstream.retry_count, 3)
            self.assertEqual(result["authorization"], "Bearer relay-test-key")
            self.assertIsNone(result["x_api_key"])

            status, result = post("/messages")
            self.assertEqual(status, 201)
            self.assertIsNone(result["authorization"])
            self.assertEqual(result["x_api_key"], "relay-test-key")

            proxy.select("local")
            upstream.retry_count = 0
            status, result = post("/v1/retry")
            self.assertEqual(status, 201)
            self.assertEqual(upstream.retry_count, 3)
            self.assertEqual(result["authorization"], "Bearer client-test-key")
            self.assertIsNone(result["x_api_key"])

            deadline = time.monotonic() + 1
            while proxy.metrics.snapshot()["active"] and time.monotonic() < deadline:
                time.sleep(0.01)
            snapshot = proxy.snapshot()
            self.assertEqual(snapshot["total"], 3)
            self.assertEqual(snapshot["retries_429"], 4)
            self.assertEqual(snapshot["queued"], 0)
            recovered = [event for event in snapshot["recent"] if event["retries"]]
            self.assertEqual(len(recovered), 2)
            self.assertTrue(all(event["state"] == "ok" for event in recovered))
            self.assertTrue(all(not event["error_detail"] for event in recovered))
            self.assertNotIn("relay-test-key", repr(snapshot))
            settings = proxy.echo_settings()
            self.assertEqual(settings["rpm"], 6000)
            self.assertTrue(settings["key_configured"])
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_lite_30_spills_to_pro_120_and_reprobes_model(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        upstream.auth_sequence = []
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}",
            f"http://127.0.0.1:{upstream.server_port}/v1",
            echo_api_key="lite-key",
        )
        proxy.select("echo")
        lite = proxy.provider_keys("echo")[0]
        proxy.update_provider_key("echo", lite["id"], 30)
        proxy.add_provider_key("echo", "pro-key", 120)

        def post(model):
            body = json.dumps({"model": model}).encode()
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=4
            )
            connection.request(
                "POST",
                "/model-fallback",
                body,
                {"Content-Type": "application/json"},
            )
            response = connection.getresponse()
            result = json.loads(response.read())
            connection.close()
            self.assertEqual(response.status, 201)
            return result["authorization"]

        try:
            self.assertEqual(post("gpt-5.6-sol"), "Bearer pro-key")
            self.assertEqual(post("gpt-5.6-sol"), "Bearer pro-key")
            self.assertEqual(post("another-model"), "Bearer pro-key")
            time.sleep(1.1)
            self.assertEqual(post("another-model"), "Bearer lite-key")

            self.assertEqual(
                upstream.auth_sequence,
                [
                    "Bearer lite-key",
                    "Bearer pro-key",
                    "Bearer pro-key",
                    "Bearer pro-key",
                    "Bearer lite-key",
                ],
            )
            keys = proxy.provider_keys("echo")
            self.assertEqual([key["rpm"] for key in keys], [30, 120])
            self.assertEqual(keys[0]["blocked_models"], 1)
            self.assertEqual(proxy.snapshot()["rpm"], 150)
            self.assertEqual(proxy.metrics.snapshot()["errors"], 0)
            self.assertEqual(proxy.metrics.snapshot()["retries"], 1)
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_plan_404_matching_and_per_key_proxy(self):
        for invalid_url in (
            "https://example.com:99999",
            "https://example.com:0",
            "https://example.com/a b",
        ):
            with self.subTest(invalid_url=invalid_url), self.assertRaises(ValueError):
                relay.ProviderRegistry.make_spec("Broken", invalid_url)
        message = (
            b"Unexpected status 404 Not Found: Model 'gpt-5.6-sol' "
            b"is not available on your plan."
        )
        self.assertTrue(model_unavailable_on_plan(404, message, "gpt-5.6-sol"))
        self.assertFalse(model_unavailable_on_plan(404, message, "another-model"))
        self.assertFalse(model_unavailable_on_plan(404, b"route not found", "gpt-5.6-sol"))
        self.assertFalse(model_unavailable_on_plan(500, message, "gpt-5.6-sol"))

        tunnel, target, headers = upstream_connection(
            urlsplit("https://api.echogate.one/v1"),
            "/v1/responses",
            "http://user:pass@127.0.0.1:8080",
        )
        try:
            self.assertEqual((tunnel.host, tunnel.port), ("127.0.0.1", 8080))
            self.assertEqual((tunnel._tunnel_host, tunnel._tunnel_port), ("api.echogate.one", 443))
            self.assertIn("Proxy-Authorization", tunnel._tunnel_headers)
            self.assertEqual(target, "/v1/responses")
            self.assertEqual(headers, {})
        finally:
            tunnel.close()

        upstream = start_server(UpstreamHandler, "Upstream")
        upstream.auth_sequence = []
        outbound = start_server(OutboundProxyHandler, "Proxy")
        outbound.requests = []
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}",
            f"http://127.0.0.1:{upstream.server_port}/v1",
            echo_api_key="lite-key",
        )
        proxy.select("echo")
        lite = proxy.provider_keys("echo")[0]
        proxy.update_provider_key("echo", lite["id"], 30)
        proxy.add_provider_key(
            "echo",
            "pro-key",
            120,
            f"http://user:pass@127.0.0.1:{outbound.server_port}",
        )
        with self.assertRaisesRegex(ValueError, "http://"):
            proxy.add_provider_key("echo", "invalid-proxy-key", 120, "socks5://127.0.0.1:1080")
        with self.assertRaisesRegex(ValueError, "port"):
            proxy.add_provider_key("echo", "invalid-proxy-port", 120, "http://127.0.0.1:0")
        try:
            body = json.dumps({"model": "gpt-5.6-sol"}).encode()
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=4
            )
            connection.request(
                "POST",
                "/model-fallback",
                body,
                {"Content-Type": "application/json"},
            )
            response = connection.getresponse()
            result = json.loads(response.read())
            connection.close()

            self.assertEqual(response.status, 201)
            self.assertTrue(result["through_proxy"])
            self.assertEqual(upstream.auth_sequence, ["Bearer lite-key"])
            self.assertEqual(len(outbound.requests), 1)
            request = outbound.requests[0]
            self.assertTrue(request["target"].startswith("http://127.0.0.1:"))
            self.assertEqual(request["authorization"], "Bearer pro-key")
            self.assertEqual(request["proxy_authorization"], "Basic dXNlcjpwYXNz")

            keys = proxy.provider_keys("echo")
            self.assertEqual([key["proxy"] for key in keys], ["Direct", f"127.0.0.1:{outbound.server_port}"])
            self.assertNotIn("user", repr(keys))
            self.assertNotIn("pass", repr(keys))
            self.assertNotIn("pro-key", repr(keys))

            proxy.remove_provider_key("echo", lite["id"])
            outbound.get_failures = 1
            models = proxy.fetch_models("echo")
            self.assertEqual(models["data"][0]["id"], "proxied-model")
            self.assertEqual(len(outbound.requests), 3)
            self.assertTrue(outbound.requests[1]["target"].endswith("/v1/models"))
            self.assertTrue(outbound.requests[2]["target"].endswith("/v1/models"))
            self.assertEqual(proxy.provider_keys("echo")[0]["retries_429"], 1)
        finally:
            for server in (proxy, outbound, upstream):
                server.shutdown()
                server.server_close()

    def test_lite_model_is_reprobed_after_cooldown(self):
        spec = relay.ProviderRegistry.make_spec(
            "EchoGate",
            "https://api.echogate.one/v1",
            provider_id="echo",
        )
        with patch("relay_runtime.MODEL_REPROBE_SECONDS", 0.05):
            registry = relay.ProviderRegistry(
                ((spec, (("lite-key", 0), ("pro-key", 0))),)
            )
            lease = registry.acquire("echo")
            try:
                first, _ = lease.runtime.acquire_attempt(lambda: False, "gpt-5.6-sol")
                self.assertEqual(first.api_key, "lite-key")
                lease.runtime.defer_attempt(
                    first,
                    1,
                    rate_limited=False,
                    block_model=True,
                    model="gpt-5.6-sol",
                )
                fallback, _ = lease.runtime.acquire_attempt(lambda: False, "gpt-5.6-sol")
                self.assertEqual(fallback.api_key, "pro-key")
                time.sleep(0.06)
                reprobe, _ = lease.runtime.acquire_attempt(lambda: False, "gpt-5.6-sol")
                self.assertEqual(reprobe.api_key, "lite-key")
                self.assertEqual(lease.runtime.key_views()[0]["blocked_models"], 0)
            finally:
                lease.release()
                registry.close()

        defaults = relay.ProviderRegistry.defaults("default-lite-key")
        try:
            self.assertEqual(defaults.key_views("echo")[0]["rpm"], 30)
            self.assertEqual(defaults.key_views("echo")[0]["proxy"], "Direct")
            echo = next(provider for provider in defaults.list() if provider.id == "echo")
            self.assertEqual((echo.rpm, echo.key_rpm, echo.effective_rpm), (0, 30, 30))
        finally:
            defaults.close()

    def test_balance_error_cools_key_until_moscow_midnight(self):
        self.assertTrue(relay_http.balance_exhausted(402, b""))
        self.assertTrue(
            relay_http.balance_exhausted(
                429, b'{"error":{"code":"insufficient_quota"}}'
            )
        )
        self.assertFalse(relay_http.balance_exhausted(429, b"rate limit exceeded"))

        spec = relay.ProviderRegistry.make_spec(
            "EchoGate",
            "https://api.echogate.one/v1",
            provider_id="echo",
        )
        registry = relay.ProviderRegistry(
            ((spec, (("lite-key", 0), ("pro-key", 0))),)
        )
        lease = registry.acquire("echo")
        before_midnight = 1785358799.0  # 2026-07-29 23:59:59 MSK
        after_midnight = 1785358801.0
        try:
            with patch("relay_runtime.time.time", return_value=before_midnight):
                lite, _ = lease.runtime.acquire_attempt(lambda: False)
                self.assertEqual(lite.api_key, "lite-key")
                self.assertTrue(
                    lease.runtime.defer_balance(lite, rate_limited=False)
                )
                pro, _ = lease.runtime.acquire_attempt(lambda: False)
                self.assertEqual(pro.api_key, "pro-key")
                self.assertAlmostEqual(
                    lease.runtime.key_views()[0]["cooldown_ms"], 1000, delta=10
                )
            with patch("relay_runtime.time.time", return_value=after_midnight):
                recovered, _ = lease.runtime.acquire_attempt(lambda: False)
                self.assertEqual(recovered.api_key, "lite-key")
        finally:
            lease.release()
            registry.close()

        upstream = start_server(UpstreamHandler, "Upstream")
        upstream.auth_sequence = []
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}",
            f"http://127.0.0.1:{upstream.server_port}/v1",
            echo_api_key="lite-key",
        )
        proxy.select("echo")
        proxy.add_provider_key("echo", "pro-key")

        def post():
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=4
            )
            connection.request(
                "POST",
                "/balance-fallback",
                b'{}',
                {"Content-Type": "application/json"},
            )
            response = connection.getresponse()
            response.read()
            status = response.status
            connection.close()
            return status

        try:
            self.assertEqual(post(), 201)
            self.assertEqual(post(), 201)
            self.assertEqual(
                upstream.auth_sequence,
                ["Bearer lite-key", "Bearer pro-key", "Bearer pro-key"],
            )
            self.assertGreater(proxy.provider_keys("echo")[0]["cooldown_ms"], 0)
            self.assertEqual(proxy.metrics.snapshot()["errors"], 0)
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_non_429_error_is_retried_without_leaking(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        try:
            started = time.monotonic()
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=2
            )
            connection.request(
                "POST",
                "/v1/rejected",
                b"{}",
                {"Content-Type": "application/json"},
            )
            response = connection.getresponse()
            body = json.loads(response.read())
            elapsed = time.monotonic() - started
            connection.close()

            self.assertEqual(response.status, 201)
            self.assertEqual(body["path"], "/v1/rejected")
            self.assertEqual(upstream.rejected_count, 3)
            self.assertLess(elapsed, 2)
            snapshot = proxy.metrics.snapshot()
            self.assertEqual(snapshot["errors"], 0)
            self.assertEqual(snapshot["retries"], 2)
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_request_error_does_not_queue_unrelated_requests(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")

        def rejected_request():
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=0.5
            )
            try:
                connection.request(
                    "POST",
                    "/v1/bad-request",
                    b"{}",
                    {"Content-Type": "application/json"},
                )
                connection.getresponse().read()
            except (OSError, http.client.HTTPException):
                pass
            finally:
                connection.close()

        thread = threading.Thread(target=rejected_request, daemon=True)
        thread.start()
        try:
            deadline = time.monotonic() + 1
            while (
                not getattr(upstream, "bad_request_count", 0)
                and time.monotonic() < deadline
            ):
                time.sleep(0.01)
            self.assertEqual(upstream.bad_request_count, 1)

            started = time.monotonic()
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=1
            )
            connection.request("GET", "/v1/models")
            response = connection.getresponse()
            response.read()
            connection.close()
            self.assertEqual(response.status, 200)
            self.assertLess(time.monotonic() - started, 1)
        finally:
            proxy.shutdown()
            proxy.server_close()
            thread.join(timeout=1)
            upstream.shutdown()
            upstream.server_close()

    def test_real_http_rpm_queue(self):
        upstream = start_server(UpstreamHandler, "EchoGate")
        upstream.arrivals = []
        upstream.arrival_lock = threading.Lock()
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}",
            f"http://127.0.0.1:{upstream.server_port}/v1",
            echo_api_key="queue-test-key",
            echo_rpm=600,
        )
        proxy.select("echo")
        barrier = threading.Barrier(7)
        statuses = []

        def post(index):
            barrier.wait()
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=4
            )
            connection.request(
                "POST",
                f"/queue/{index}",
                b"{}",
                {"Content-Type": "application/json"},
            )
            response = connection.getresponse()
            response.read()
            statuses.append(response.status)
            connection.close()

        threads = [
            threading.Thread(target=post, args=(index,), daemon=True)
            for index in range(6)
        ]
        try:
            for thread in threads:
                thread.start()
            started = time.monotonic()
            barrier.wait()
            for thread in threads:
                thread.join(timeout=4)
            elapsed = time.monotonic() - started

            self.assertTrue(all(not thread.is_alive() for thread in threads))
            self.assertEqual(statuses, [201] * 6)
            self.assertEqual(len(upstream.arrivals), 6)
            arrivals = sorted(upstream.arrivals)
            self.assertTrue(
                all(later - earlier >= 0.07 for earlier, later in zip(arrivals, arrivals[1:]))
            )
            self.assertGreaterEqual(elapsed, 0.4)
            snapshot = proxy.snapshot()
            self.assertEqual(snapshot["active"], 0)
            self.assertEqual(snapshot["queued"], 0)
            self.assertEqual(snapshot["successes"], 6)
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_shutdown_closes_retry_waiters(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        try:
            for provider_id, path in (("local", "/v1/always-429"), ("echo", "/always-429")):
                with self.subTest(provider=provider_id):
                    proxy = start_proxy(
                        f"http://127.0.0.1:{upstream.server_port}",
                        f"http://127.0.0.1:{upstream.server_port}/v1",
                        echo_api_key="shutdown-test-key",
                    )
                    proxy.select(provider_id)
                    result = []

                    def post():
                        try:
                            connection = http.client.HTTPConnection(
                                "127.0.0.1", proxy.server_port, timeout=3
                            )
                            connection.request(
                                "POST",
                                path,
                                b"{}",
                                {"Content-Type": "application/json"},
                            )
                            response = connection.getresponse()
                            response.read()
                            result.append(response.status)
                        except Exception as error:
                            result.append(error)

                    thread = threading.Thread(target=post, daemon=True)
                    thread.start()
                    deadline = time.monotonic() + 1
                    while (
                        proxy.metrics.snapshot()["retries_429"] < 1
                        and time.monotonic() < deadline
                    ):
                        time.sleep(0.01)
                    self.assertEqual(proxy.metrics.snapshot()["retries_429"], 1)

                    stopped_at = time.monotonic()
                    proxy.shutdown()
                    proxy.server_close()
                    thread.join(timeout=1)
                    self.assertFalse(thread.is_alive())
                    self.assertLess(time.monotonic() - stopped_at, 1)
                    self.assertTrue(result)
                    self.assertNotIsInstance(result[0], TimeoutError)
                    snapshot = proxy.metrics.snapshot()
                    self.assertEqual(snapshot["active"], 0)
                    self.assertEqual(snapshot["cancelled"], 1)
        finally:
            upstream.shutdown()
            upstream.server_close()

    def test_shutdown_interrupts_active_upstream_io(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        upstream.hang_started = threading.Event()
        upstream.hang_release = threading.Event()
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        result = []

        def post():
            try:
                connection = http.client.HTTPConnection(
                    "127.0.0.1", proxy.server_port, timeout=5
                )
                connection.request(
                    "POST",
                    "/v1/hang",
                    b"{}",
                    {"Content-Type": "application/json"},
                )
                result.append(connection.getresponse().read())
            except Exception as error:
                result.append(error)

        thread = threading.Thread(target=post, daemon=True)
        thread.start()
        try:
            self.assertTrue(upstream.hang_started.wait(1))
            stopped_at = time.monotonic()
            proxy.shutdown()
            proxy.server_close()
            thread.join(timeout=1)
            self.assertFalse(thread.is_alive())
            self.assertLess(time.monotonic() - stopped_at, 1)
            self.assertTrue(result)
            self.assertEqual(proxy.metrics.snapshot()["active"], 0)
        finally:
            upstream.hang_release.set()
            upstream.shutdown()
            upstream.server_close()


    def test_reset_key_cooldown_preserves_rpm_window_and_retry_telemetry(self):
        spec = relay.ProviderRegistry.make_spec(
            "Provider", "https://provider.example/v1", provider_id="provider"
        )
        registry = relay.ProviderRegistry(((spec, (("reset-key", 600, ""),)),))
        lease = registry.acquire("provider")
        try:
            runtime = lease.runtime
            attempt, _ = runtime.acquire_attempt(lambda: False, "blocked-model")
            runtime.defer_attempt(attempt, 60, rate_limited=True)
            runtime.defer_attempt(
                attempt,
                60,
                rate_limited=True,
                block_model=True,
                model="blocked-model",
            )
            runtime.defer_balance(attempt, rate_limited=True)
            slot = attempt._slot
            self.assertIsNotNone(slot)
            next_slot = slot.next_slot
            self.assertEqual(len(slot.starts), 1)
            self.assertEqual(slot.retries_429, 3)

            result = []
            errors = []
            started = time.monotonic()

            def acquire_again():
                try:
                    result.append(
                        runtime.acquire_attempt(
                            lambda: time.monotonic() - started > 0.4,
                            "blocked-model",
                        )
                    )
                except Exception as error:
                    errors.append(error)

            waiter = threading.Thread(target=acquire_again)
            waiter.start()
            time.sleep(0.02)
            registry.reset_key_cooldown(
                "provider", relay_runtime.key_fingerprint("reset-key")
            )
            self.assertEqual(slot.cooldown_until, 0)
            self.assertEqual(slot.balance_cooldown_until, 0)
            self.assertEqual(slot.blocked_models, {})
            self.assertEqual(slot.next_slot, next_slot)
            self.assertEqual(len(slot.starts), 1)
            self.assertEqual(slot.retries_429, 3)
            waiter.join(0.4)

            self.assertFalse(waiter.is_alive())
            self.assertEqual(errors, [])
            self.assertEqual(result[0][0].api_key, "reset-key")
            self.assertGreaterEqual(time.monotonic() - started, 0.05)
            self.assertEqual(len(slot.starts), 2)
            self.assertEqual(slot.retries_429, 3)
        finally:
            lease.release()
            registry.close()


class ConfigPersistenceTest(unittest.TestCase):
    def test_tunnel_token_and_model_allowlist_round_trip_encrypted(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            config_path = Path(temporary_directory) / "config.dpapi"
            first = relay.RelayServer(
                ("127.0.0.1", 0),
                relay.RelayHandler,
                echo_api_key="",
                config_path=config_path,
                history=False,
            )
            try:
                first.set_tunnel_allowed_models(("gpt-private", "claude-private"))
                first.set_tunnel_rpm_per_ip(45)
                profile = "v1.23456." + ("c" * 48)
                first.set_tunnel_publisher_profile(profile)
                token = first.tunnel_access_token()
                self.assertEqual(first.tunnel_rpm_per_ip(), 45)
                self.assertEqual(first.tunnel_publisher_profile(), profile)
                self.assertNotIn(token, json.dumps(first.tunnel_snapshot()))
            finally:
                first.server_close()

            encrypted = config_path.read_bytes()
            self.assertNotIn(token.encode(), encrypted)
            self.assertNotIn(b"gpt-private", encrypted)
            self.assertNotIn(profile.encode(), encrypted)
            second = relay.RelayServer(
                ("127.0.0.1", 0),
                relay.RelayHandler,
                echo_api_key="",
                config_path=config_path,
                history=False,
            )
            try:
                self.assertEqual(second.tunnel_access_token(), token)
                self.assertEqual(
                    second.tunnel_allowed_models(),
                    ("gpt-private", "claude-private"),
                )
                self.assertEqual(second.tunnel_snapshot()["allowed_count"], 2)
                self.assertEqual(second.tunnel_rpm_per_ip(), 45)
                self.assertEqual(second.tunnel_publisher_profile(), profile)
                self.assertEqual(second.tunnel_snapshot()["rpm_per_ip"], 45)
            finally:
                second.server_close()

    def test_environment_key_is_only_attached_to_builtin_echogate(self):
        registry = relay.ProviderRegistry.from_config(
            {
                "schema": 1,
                "active": "echo",
                "providers": [
                    {
                        "id": "echo",
                        "name": "Other",
                        "upstream": "https://other-provider.example/v1",
                        "auth_mode": "auto",
                        "cache_1h": False,
                        "rpm": 0,
                        "keys": [],
                    }
                ],
            },
            "env-lite-key",
        )
        try:
            self.assertEqual(registry.export_config()["providers"][0]["keys"], [])
        finally:
            registry.close()

    def test_restart_restores_providers_keys_rpm_proxy_and_active_provider(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            config_path = Path(temporary_directory) / "config.dpapi"
            first = relay.RelayServer(
                ("127.0.0.1", 0),
                relay.RelayHandler,
                echo_api_key="env-lite-a",
                config_path=config_path,
                history=False,
            )
            try:
                first.update_provider("echo", rpm=150)
                with self.assertRaisesRegex(ValueError, "URL is fixed"):
                    first.update_provider(
                        "echo", upstream="https://other-provider.example/v1"
                    )
                first.add_provider_key(
                    "echo",
                    "persisted-pro-key",
                    rpm=120,
                    proxy_url="http://proxy-user:proxy-pass@127.0.0.1:8888",
                )
                custom = first.add_provider(
                    name="Custom",
                    upstream="https://custom.example/v1",
                    rpm=45,
                    auth_mode="bearer",
                )
                first.add_provider_key(custom.id, "custom-persisted-key", rpm=45)
                first.select(custom.id)
                self.assertEqual(first.config_error, "")
            finally:
                first.server_close()

            encrypted = config_path.read_bytes()
            for secret in (
                b"env-lite-a",
                b"persisted-pro-key",
                b"proxy-user",
                b"proxy-pass",
                b"custom.example",
            ):
                self.assertNotIn(secret, encrypted)

            second = relay.RelayServer(
                ("127.0.0.1", 0),
                relay.RelayHandler,
                echo_api_key="env-lite-b",
                config_path=config_path,
                history=False,
            )
            try:
                providers = {provider.id: provider for provider in second.providers()}
                self.assertTrue(providers[custom.id].active)
                self.assertEqual(providers["echo"].rpm, 150)
                self.assertEqual(providers[custom.id].rpm, 45)
                self.assertEqual(
                    [key["rpm"] for key in second.provider_keys("echo")], [30, 120]
                )
                self.assertEqual(
                    [key["proxy"] for key in second.provider_keys("echo")],
                    ["Direct", "127.0.0.1:8888"],
                )
                environment_id = second.provider_keys("echo")[0]["id"]
                with self.assertRaisesRegex(ValueError, "30 RPM"):
                    second.update_provider_key("echo", environment_id, 31)
                with self.assertRaisesRegex(ValueError, "Windows environment"):
                    second.remove_provider_key("echo", environment_id)

                lease = second.registry.acquire("echo")
                try:
                    lite, _ = lease.runtime.acquire_attempt(lambda: False, "probe")
                    self.assertEqual(lite.api_key, "env-lite-b")
                    lease.runtime.defer_attempt(
                        lite,
                        1,
                        rate_limited=False,
                        block_model=True,
                        model="probe",
                    )
                    pro, _ = lease.runtime.acquire_attempt(lambda: False, "probe")
                    self.assertEqual(pro.api_key, "persisted-pro-key")
                finally:
                    lease.release()
            finally:
                second.server_close()

            normalized = config_path.read_bytes()
            self.assertNotIn(b"env-lite-a", normalized)
            self.assertNotIn(b"env-lite-b", normalized)

    def test_key_priority_order_is_exported_and_persists(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            config_path = Path(temporary_directory) / "config.dpapi"
            first = relay.RelayServer(
                ("127.0.0.1", 0),
                relay.RelayHandler,
                echo_api_key="",
                config_path=config_path,
                history=False,
            )
            try:
                fingerprints = [
                    first.add_provider_key("echo", key, rpm)
                    for key, rpm in (
                        ("priority-key-a", 30),
                        ("priority-key-b", 60),
                        ("priority-key-c", 120),
                    )
                ]
                first.move_provider_key("echo", fingerprints[2], -1)
                expected = [fingerprints[0], fingerprints[2], fingerprints[1]]
                self.assertEqual(
                    [row["id"] for row in first.registry.key_views("echo")],
                    expected,
                )
                exported = first.registry.export_config()
                self.assertEqual(
                    [
                        relay_runtime.key_fingerprint(item["key"])
                        for item in exported["providers"][1]["keys"]
                    ],
                    expected,
                )
            finally:
                first.server_close()

            encrypted = config_path.read_bytes()
            self.assertNotIn(b"priority-key-", encrypted)
            second = relay.RelayServer(
                ("127.0.0.1", 0),
                relay.RelayHandler,
                echo_api_key="",
                config_path=config_path,
                history=False,
            )
            try:
                self.assertEqual(
                    [row["id"] for row in second.provider_keys("echo")],
                    expected,
                )
            finally:
                second.server_close()

    def test_failed_atomic_save_keeps_previous_file_and_reports_warning(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            config_path = Path(temporary_directory) / "config.dpapi"
            server = relay.RelayServer(
                ("127.0.0.1", 0),
                relay.RelayHandler,
                echo_api_key="env-lite",
                config_path=config_path,
                history=False,
            )
            try:
                server.select("echo")
                previous = config_path.read_bytes()
                with patch("relay_config.os.replace", side_effect=OSError("simulated")):
                    server.add_provider_key("echo", "session-only-key", rpm=120)
                self.assertIn("not saved", server.config_error)
                self.assertEqual(config_path.read_bytes(), previous)
                with patch("relay_config.os.chmod", side_effect=OSError("simulated")):
                    server.add_provider_key("echo", "chmod-session-key", rpm=120)
                self.assertEqual(config_path.read_bytes(), previous)
            finally:
                server.server_close()

    def test_corrupted_config_fails_closed(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            config_path = Path(temporary_directory) / "config.dpapi"
            config_path.write_bytes(b"not-a-provider-config")
            with self.assertRaises(ConfigError):
                relay.RelayServer(
                    ("127.0.0.1", 0),
                    relay.RelayHandler,
                    echo_api_key="env-lite",
                    config_path=config_path,
                    history=False,
                )
            self.assertEqual(config_path.read_bytes(), b"not-a-provider-config")


class RelayTUITest(unittest.IsolatedAsyncioTestCase):
    async def test_tunnel_tab_is_usable_at_compact_and_wide_sizes(self):
        server = relay.RelayServer(
            ("127.0.0.1", 0),
            relay.RelayHandler,
            echo_api_key="",
            config=False,
            history=False,
        )
        server.fetch_models = lambda _provider_id: {
            "data": [{"id": "gpt-tunnel"}, {"id": "claude-tunnel"}]
        }
        server.set_tunnel_allowed_models(("gpt-tunnel",))
        rpm = {"value": 0}
        server.tunnel_rpm_per_ip = lambda: rpm["value"]
        server.set_tunnel_rpm_per_ip = lambda value: rpm.update(value=value)
        profile = {"value": "private-publisher-profile"}
        server.tunnel_publisher_profile = lambda: profile["value"]
        server.set_tunnel_publisher_profile = lambda value: profile.update(value=value)
        profile_urls = tuple(
            f"https://luxuryprivate.duckdns.org/model-tunnel/{slug}/v1"
            for slug in ("a" * 48, "b" * 48)
        )
        tunnel_state = {
            "state": "running",
            "url": profile_urls[0],
            "allowed_count": 1,
            "error": "",
            "route_available": True,
        }
        server.tunnel_snapshot = lambda: dict(tunnel_state)
        copied_urls = []
        try:
            for index, (size, compact) in enumerate(
                (((80, 24), True), ((120, 35), False))
            ):
                with self.subTest(size=size):
                    rpm["value"] = 0
                    profile["value"] = "private-publisher-profile"
                    tunnel_state.update(
                        state="running",
                        url=profile_urls[index],
                        error="",
                    )
                    app = RelayApp(server)
                    app.copy_to_clipboard = copied_urls.append
                    async with app.run_test(size=size) as pilot:
                        app.query_one("#main-tabs").active = "tunnel"
                        for _ in range(20):
                            await pilot.pause(0.05)
                            if app.query_one("#tunnel-models", SelectionList).option_count == 2:
                                break
                        models = app.query_one("#tunnel-models", SelectionList)
                        self.assertEqual(models.option_count, 2)
                        self.assertEqual(tuple(models.selected), ("gpt-tunnel",))
                        self.assertEqual(app.screen.has_class("compact"), compact)
                        self.assertEqual(app._tunnel_url, profile_urls[index])
                        self.assertIn(
                            profile_urls[index],
                            str(app.query_one("#tunnel-status", Static).content),
                        )
                        self.assertFalse(
                            app.query_one("#tunnel-copy-url", Button).disabled
                        )
                        tunnel_state["route_available"] = False
                        app._refresh_tunnel()
                        self.assertIn(
                            "Paused",
                            str(app.query_one("#tunnel-status", Static).content),
                        )
                        self.assertTrue(
                            app.query_one("#tunnel-copy-url", Button).disabled
                        )
                        tunnel_state["route_available"] = True
                        app._refresh_tunnel()
                        await pilot.click("#tunnel-copy-url")
                        controls = [
                            app.query_one(f"#tunnel-{name}", Button)
                            for name in (
                                "start",
                                "stop",
                                "refresh-models",
                                "copy-url",
                                "copy-key",
                                "rotate-key",
                            )
                        ]
                        rpm_input = app.query_one("#tunnel-rpm-input", Input)
                        save_rpm = app.query_one("#tunnel-save-rpm", Button)
                        controls.extend((rpm_input, save_rpm))
                        self.assertTrue(all(control.region.width > 0 for control in controls))
                        self.assertEqual(len({control.region.y for control in controls}), 2 if compact else 1)
                        self.assertEqual(rpm_input.value, "0")
                        self.assertEqual(str(rpm_input.border_title), "Per-IP RPM")
                        self.assertFalse(rpm_input.disabled)
                        self.assertFalse(save_rpm.disabled)
                        rpm_input.value = "37"
                        await pilot.click("#tunnel-save-rpm")
                        await pilot.pause()
                        self.assertEqual(rpm["value"], 37)
                        rpm_input.value = "0"
                        await pilot.click("#tunnel-save-rpm")
                        await pilot.pause()
                        self.assertEqual(rpm["value"], 0)
                        profile_input = app.query_one("#tunnel-profile-input", Input)
                        save_profile = app.query_one("#tunnel-save-profile", Button)
                        self.assertEqual(profile_input.value, "private-publisher-profile")
                        self.assertTrue(profile_input.password)
                        self.assertEqual(
                            str(profile_input.border_title), "Publisher profile"
                        )
                        self.assertTrue(profile_input.disabled)
                        self.assertTrue(save_profile.disabled)
                        self.assertEqual(profile_input.region.y, save_profile.region.y)
                        self.assertTrue(
                            profile_input.region.width > 0 and save_profile.region.width > 0
                        )
                        tunnel_state.update(state="stopped", url="", error="")
                        app._refresh_tunnel()
                        self.assertFalse(profile_input.disabled)
                        self.assertFalse(save_profile.disabled)
                        profile_input.value = "new-private-profile"
                        await pilot.click("#tunnel-save-profile")
                        await pilot.pause()
                        self.assertEqual(profile["value"], "new-private-profile")
                        self.assertEqual(app._tunnel_url, "")
                        starts = []

                        def start_tunnel():
                            starts.append(True)
                            tunnel_state.update(
                                state="running",
                                url=profile_urls[index],
                                error="",
                            )
                            return dict(tunnel_state)

                        server.start_tunnel = start_tunnel
                        await pilot.click("#tunnel-start")
                        for _ in range(20):
                            await pilot.pause(0.05)
                            if not app._tunnel_busy:
                                break
                        self.assertEqual(starts, [True])
                        self.assertEqual(app._tunnel_url, profile_urls[index])
                        self.assertTrue(profile_input.disabled)
                        self.assertTrue(save_profile.disabled)
                        for invalid_url in (
                            f"https://foreign.invalid/model-tunnel/{'c' * 48}/v1",
                            f"https://luxuryprivate.duckdns.org/model-tunnel/{'D' * 48}/v1",
                            profile_urls[index] + "/",
                        ):
                            tunnel_state["url"] = invalid_url
                            app._refresh_tunnel()
                            self.assertEqual(app._tunnel_url, "")
                            self.assertTrue(
                                app.query_one("#tunnel-copy-url", Button).disabled
                            )
                            self.assertNotIn(
                                invalid_url,
                                str(app.query_one("#tunnel-status", Static).content),
                            )
                        tunnel_state["url"] = profile_urls[index]
                        app._refresh_tunnel()
                        self.assertGreaterEqual(models.region.height, 6)
                        screenshot = app.export_screenshot()
                        self.assertIn("gpt-tunnel", screenshot)
                        self.assertIn("claude-tunnel", screenshot)
                        self.assertNotIn("private-publisher-profile", screenshot)
                        self.assertNotIn("new-private-profile", screenshot)
                        self.assertNotIn(server.tunnel_access_token(), screenshot)
            self.assertEqual(copied_urls, list(profile_urls))
        finally:
            server.server_close()

    async def test_controls_metrics_and_responsive_layout(self):
        for state, label, color in (
            ("ok", "Done", "success"),
            ("active", "Live", "live"),
            ("queued", "Queued", "waiting"),
            ("retry", "Retrying", "waiting"),
            ("error", "Error", "error"),
            ("cancelled", "Cancelled", "muted"),
        ):
            cell = _state_cell(state)
            self.assertEqual(cell.plain, f"● {label}")
            self.assertEqual(str(cell.style), PALETTE[color])
        server = relay.RelayServer(
            ("127.0.0.1", 0),
            relay.RelayHandler,
            echo_api_key="",
            config=False,
            history=False,
        )
        try:
            server.select("local")
            app = RelayApp(server)
            async with app.run_test(size=(120, 35)) as pilot:
                await pilot.pause()
                self.assertEqual(app.query_one("#providers-table", DataTable).row_count, 2)
                app.query_one("#main-tabs").active = "providers"
                await pilot.pause()
                app.query_one("#providers-table", DataTable).move_cursor(row=1)
                await pilot.pause()
                app._refresh_keys()
                app.query_one("#key-input", Input).value = "ui-test-key"
                app.query_one("#key-rpm-input", Input).value = "120"
                app.query_one("#key-proxy-input", Input).value = (
                    "http://ui-user:ui-pass@127.0.0.1:8080"
                )
                app._selected_provider_id = "echo"
                await pilot.click("#add-key")
                await pilot.pause()
                self.assertEqual(server.provider_keys("echo")[0]["rpm"], 120)
                self.assertEqual(
                    server.provider_keys("echo")[0]["proxy"], "127.0.0.1:8080"
                )
                screenshot = app.export_screenshot()
                self.assertNotIn("ui-test-key", screenshot)
                self.assertNotIn("ui-user", screenshot)
                self.assertNotIn("ui-pass", screenshot)

                app.query_one("#key-rpm-input", Input).value = "121"
                app._refresh_live()
                self.assertEqual(app.query_one("#key-rpm-input", Input).value, "121")
                await pilot.click("#update-key-settings")
                await pilot.pause()
                self.assertEqual(server.provider_keys("echo")[0]["rpm"], 121)
                echo = next(provider for provider in server.providers() if provider.id == "echo")
                self.assertEqual(echo.effective_rpm, 121)
                self.assertEqual(
                    server.provider_keys("echo")[0]["proxy"], "127.0.0.1:8080"
                )
                app.query_one("#key-proxy-input", Input).value = "direct"
                await pilot.click("#update-key-settings")
                await pilot.pause()
                self.assertEqual(server.provider_keys("echo")[0]["proxy"], "Direct")

                app._selected_provider_id = "echo"
                app._tunnel_models_loaded = True
                await pilot.click("#activate-provider")
                await pilot.pause()
                self.assertEqual(server.provider()[0], "EchoGate")
                self.assertFalse(app._tunnel_models_loaded)
                await pilot.click("#--content-tab-dashboard")
                await pilot.pause()

                request_id = server.metrics.begin(
                    "echo", "EchoGate", "POST", "/v1/responses"
                )
                server.metrics.request(request_id, "gpt-live-test", 64, False)
                await pilot.pause(0.35)
                live_screen = app.export_screenshot()
                self.assertIn("gpt-live-test", live_screen)
                self.assertIn(
                    "actual RPM",
                    str(app.query_one("#metric-throughput", Static).content),
                )
                self.assertIn(
                    "Limit 121",
                    str(app.query_one("#metric-throughput", Static).content),
                )
                server.metrics.finish(
                    request_id,
                    200,
                    12.5,
                    128,
                    relay.TokenUsage(
                        input_tokens=100,
                        output_tokens=50,
                        cached_tokens=20,
                        reasoning_tokens=10,
                        total_tokens=150,
                        context_tokens=120,
                    ),
                )
                for index in range(2, 10):
                    request_id = server.metrics.begin(
                        "echo",
                        "EchoGate",
                        "GET",
                        f"/v1/a-very-long-path-{index}-that-must-not-widen-the-table",
                    )
                    server.metrics.finish(
                        request_id,
                        200,
                        12.5,
                        128,
                        relay.TokenUsage(),
                    )
                await pilot.pause(0.35)
                table = app.query_one("#activity", DataTable)
                self.assertEqual(table.row_count, 9)
                self.assertEqual(table.get_cell("request:1", "state").plain, "● Done")
                self.assertEqual(table.get_cell("request:1", "context").plain, "120")
                self.assertNotEqual(table.get_cell("request:1", "token_speed").plain, "—")
                await pilot.pause(0.35)
                self.assertEqual(table.row_count, 9)
                table.move_cursor(row=0, column=0)
                await pilot.click("#activity", times=2)
                await pilot.pause()
                detail = app.screen.query_one("#event-detail", Static)
                self.assertIn("Total tokens", str(detail.content))
                await pilot.click("#detail-close")
                await pilot.pause()

            compact = RelayApp(server)
            async with compact.run_test(size=(80, 24)) as pilot:
                await pilot.pause()
                self.assertTrue(compact.screen.has_class("compact"))
                activity = compact.query_one("#activity", DataTable)
                self.assertTrue(activity.is_mounted)
                self.assertGreaterEqual(activity.region.height, 5)
                self.assertEqual(activity.get_cell("request:1", "state").plain, "● Done")
                self.assertEqual(activity.get_cell("request:1", "rpm").plain, "9")
                self.assertEqual(
                    tuple(key.value for key in activity.columns)[:5],
                    ("state", "model", "token_speed", "context", "rpm"),
                )
                compact_screen = compact.export_screenshot()
                for visible in ("State", "Model", "Tok/s", "Context", "RPM"):
                    self.assertIn(visible, compact_screen)
                compact.query_one("#main-tabs").active = "providers"
                await pilot.pause()
                compact.query_one("#providers-scroll").scroll_end(animate=False)
                await pilot.pause()
                self.assertEqual(compact.query_one("#key-editor").region.height, 7)
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
                ):
                    self.assertGreater(compact.query_one(control).region.width, 0)
                compact_inputs = [
                    compact.query_one(control)
                    for control in ("#key-input", "#key-rpm-input", "#key-proxy-input")
                ]
                compact_actions = [
                    compact.query_one(control)
                    for control in (
                        "#add-key",
                        "#update-key-settings",
                        "#remove-key",
                        "#move-key-up",
                        "#move-key-down",
                        "#reset-key-cooldown",
                    )
                ]
                self.assertEqual(len({control.region.y for control in compact_inputs}), 1)
                self.assertEqual(len({control.region.y for control in compact_actions}), 1)
                self.assertGreater(
                    compact_actions[0].region.y, compact_inputs[0].region.y
                )
                self.assertEqual(str(compact.query_one("#add-key", Button).label), "Add key")
                self.assertEqual(
                    str(compact.query_one("#update-key-settings", Button).label), "Save"
                )
                self.assertEqual(str(compact.query_one("#remove-key", Button).label), "Remove")

            medium = RelayApp(server)
            async with medium.run_test(size=(100, 30)) as pilot:
                medium.query_one("#main-tabs").active = "providers"
                await pilot.pause()
                medium.query_one("#providers-scroll").scroll_end(animate=False)
                await pilot.pause()
                self.assertFalse(medium.screen.has_class("compact"))
                editor = medium.query_one("#key-editor")
                controls = [
                    medium.query_one(control)
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
                self.assertEqual(len({control.region.y for control in controls}), 1)
                self.assertTrue(
                    all(
                        editor.region.x <= control.region.x
                        and control.region.right <= editor.region.right
                        for control in controls
                    )
                )
        finally:
            server.server_close()

    async def test_models_modal_loads_and_filters(self):
        upstream = start_server(UpstreamHandler, "Local")
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}",
            f"http://127.0.0.1:{upstream.server_port}/v1",
        )
        try:
            proxy.select("local")
            app = RelayApp(proxy)
            async with app.run_test(size=(80, 24)) as pilot:
                app.query_one("#main-tabs").active = "providers"
                await pilot.pause()
                app.query_one("#providers-table", DataTable).move_cursor(row=0)
                await pilot.pause()
                await pilot.click("#provider-models")
                for _ in range(30):
                    await pilot.pause(0.1)
                    if app.screen.query_one("#models-list", OptionList).option_count == 3:
                        break
                models = app.screen.query_one("#models-list", OptionList)
                self.assertEqual(models.option_count, 3)
                model_filter = app.screen.query_one("#models-filter", Input)
                self.assertFalse(model_filter.disabled)
                model_filter.value = "sonnet"
                await pilot.pause()
                self.assertEqual(models.option_count, 1)
                screenshot = app.export_screenshot()
                self.assertIn("claude-sonnet-test", screenshot)
                self.assertNotIn("gpt-test", screenshot)
                await pilot.click("#models-close")
                await pilot.pause()
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()


class RelayMetricsTest(unittest.TestCase):
    def test_live_context_and_token_speed(self):
        inspector = relay.ResponseInspector("text/event-stream")
        inspector.feed(
            b'data: {"message":{"usage":{"input_tokens":13,'
            b'"cache_read_input_tokens":100}}}\n\n'
        )
        inspector.feed(
            b'data: {"usage":{"output_tokens":50,"reasoning_tokens":12}}\n\n'
        )
        usage = inspector.finish()
        self.assertEqual(usage.context_tokens, 113)
        self.assertEqual(usage.cached_tokens, 100)
        self.assertEqual(usage.output_tokens, 50)
        self.assertEqual(usage.total_tokens, 163)

        anthropic = relay.ResponseInspector("text/event-stream")
        anthropic.feed(
            b'data: {"type":"message_start","message":{"usage":'
            b'{"output_tokens":1}}}\n\n'
        )
        self.assertIsNone(anthropic.terminal_at)
        with patch("relay_http.time.monotonic", return_value=105.0):
            anthropic.feed(
                b'data: {"type":"message_delta","usage":'
                b'{"output_tokens":50}}\n\n'
            )
        self.assertEqual(anthropic.terminal_at, 105.0)

        with patch("relay_runtime.time.monotonic", return_value=90.0):
            metrics = relay.RelayMetrics()
            request_id = metrics.begin("echo", "EchoGate", "POST", "/v1/responses")
            metrics.request(request_id, "gpt-test", 1000, True)
        with patch("relay_runtime.time.monotonic", return_value=100.0):
            metrics.response(
                request_id,
                "echo",
                200,
                10,
                True,
                generation_started_at=95.0,
            )
        metrics.tokens(request_id, usage)
        self.assertEqual(metrics.snapshot()["live"][0]["tokens_per_second"], 0)
        metrics.tokens(request_id, usage, terminal_at=105.0)
        with patch("relay_runtime.time.monotonic", return_value=120.0):
            live = metrics.snapshot()["live"][0]
            event = metrics.finish(request_id, 200, 20, 100, usage)
        self.assertEqual(live["context_tokens"], 113)
        self.assertEqual(live["tokens_per_second"], 5.0)
        self.assertEqual(event["context_tokens"], 113)
        self.assertEqual(event["tokens_per_second"], 5.0)

    def test_errors_survive_client_cancellation(self):
        metrics = relay.RelayMetrics()
        request_id = metrics.begin("echo", "EchoGate", "POST", "/v1/responses")
        metrics.finish(
            request_id,
            429,
            10,
            0,
            relay.TokenUsage(),
            cancelled=True,
        )
        request_id = metrics.begin("echo", "EchoGate", "POST", "/v1/responses")
        metrics.finish(
            request_id,
            None,
            10,
            0,
            relay.TokenUsage(),
            cancelled=True,
        )
        request_id = metrics.begin("local", "Local", "GET", "/v1/models")
        metrics.finish(
            request_id,
            200,
            10,
            10,
            relay.TokenUsage(),
        )

        snapshot = metrics.snapshot()
        self.assertEqual(snapshot["errors"], 1)
        self.assertEqual(snapshot["cancelled"], 2)
        self.assertEqual(snapshot["successes"], 1)
        self.assertEqual(snapshot["active"], 0)
        self.assertEqual(snapshot["recent"][-3]["state"], "error")
        self.assertEqual(snapshot["recent"][-2]["state"], "cancelled")
        self.assertEqual(snapshot["recent"][-1]["state"], "ok")


class HistoryStoreTest(unittest.TestCase):
    @staticmethod
    def event(error_detail=""):
        return {
            "timestamp": time.time(),
            "provider_id": "echo",
            "provider": "EchoGate",
            "model": "gpt-test",
            "method": "POST",
            "path": "/v1/responses",
            "status": 502,
            "state": "error",
            "cache_1h": True,
            "latency_ms": 12.5,
            "queue_ms": 1.5,
            "retries": 1,
            "retries_429": 0,
            "bytes_in": 10,
            "bytes_out": 20,
            "input_tokens": 3,
            "context_tokens": 5,
            "output_tokens": 4,
            "cached_tokens": 2,
            "reasoning_tokens": 1,
            "total_tokens": 7,
            "tokens_per_second": 12.5,
            "error_detail": error_detail,
        }

    def test_migrates_old_schema_and_never_persists_arbitrary_detail(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            path = f"{temporary_directory}/history.db"
            connection = sqlite3.connect(path)
            connection.execute(
                """
                CREATE TABLE request_history (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    timestamp REAL NOT NULL,
                    provider_id TEXT NOT NULL
                )
                """
            )
            connection.close()

            store = HistoryStore(path)
            secret = "SECRET_SENTINEL_MUST_NOT_BE_STORED"
            self.assertTrue(store.record(self.event(secret)))
            self.assertTrue(
                store.record(
                    self.event("Provider connection failed · retrying in 1.0s")
                )
            )
            recent = store.recent("all")
            self.assertEqual(len(recent), 2)
            self.assertNotIn(secret, repr(recent))
            self.assertEqual(recent[0]["cache_1h"], 1)
            self.assertEqual(recent[0]["error_detail"], "Provider connection failed · retrying in 1.0s")
            self.assertEqual(recent[1]["error_detail"], "")
            stats = store.stats("all")
            self.assertEqual(stats["requests"], 2)
            self.assertEqual(stats["retries"], 2)
            self.assertEqual(stats["context_tokens"], 10)
            self.assertEqual(stats["tokens_per_second"], 12.5)

            store.close()
            self.assertFalse(store.record(self.event()))
            self.assertEqual(store.recent("all"), [])
            self.assertEqual(store.stats("all")["requests"], 0)

    def test_restart_restores_persistent_requests_to_dashboard_recent(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            store = HistoryStore(f"{temporary_directory}/history.db")
            older = self.event()
            older.update(timestamp=1_700_000_000, model="older-model")
            newer = self.event()
            newer.update(timestamp=1_700_000_001, model="newer-model")
            self.assertTrue(store.record(older))
            self.assertTrue(store.record(newer))

            server = relay.RelayServer(
                ("127.0.0.1", 0),
                relay.RelayHandler,
                config=False,
                history=store,
                echo_api_key="",
            )
            try:
                recent = server.snapshot()["recent"]
                self.assertEqual(
                    [event["model"] for event in recent],
                    ["older-model", "newer-model"],
                )
                self.assertTrue(recent[-1]["cache_1h"])
                self.assertLess(recent[-1]["seq"], 0)
                self.assertNotIn("id", recent[-1])
            finally:
                server.server_close()

    def test_sqlite_telemetry_failure_does_not_break_server(self):
        class BrokenHistory:
            calls = 0

            def record(self, _event):
                self.calls += 1
                if self.calls == 1:
                    raise sqlite3.OperationalError("simulated write failure")
                return True

            def close(self):
                pass

        server = relay.RelayServer(
            ("127.0.0.1", 0),
            relay.RelayHandler,
            config=False,
            history=BrokenHistory(),
            echo_api_key="",
        )
        try:
            server.record(self.event())
            self.assertEqual(server.snapshot()["history_error"], "History write failed")
            server.record(self.event())
            self.assertEqual(server.snapshot()["history_error"], "")
        finally:
            server.server_close()


class EchoGateGateTest(unittest.TestCase):
    def test_fifo_queue_and_live_rpm_change(self):
        gate = relay.EchoGateGate(1)
        gate.acquire(lambda: False)
        order = []
        admitted_at = []

        def acquire(index):
            gate.acquire(lambda: False)
            order.append(index)
            admitted_at.append(time.monotonic())

        threads = []
        for index in range(3):
            thread = threading.Thread(target=acquire, args=(index,), daemon=True)
            thread.start()
            threads.append(thread)
            deadline = time.monotonic() + 1
            while gate.snapshot()["queued"] < index + 1 and time.monotonic() < deadline:
                time.sleep(0.005)
            self.assertEqual(gate.snapshot()["queued"], index + 1)

        gate.set_rpm(600)
        for thread in threads:
            thread.join(timeout=1)
        gate.close()

        self.assertEqual(order, [0, 1, 2])
        self.assertTrue(all(not thread.is_alive() for thread in threads))
        self.assertGreaterEqual(admitted_at[1] - admitted_at[0], 0.07)
        self.assertGreaterEqual(admitted_at[2] - admitted_at[1], 0.07)
        self.assertEqual(gate.snapshot()["queued"], 0)

    def test_close_wakes_indefinite_waiter(self):
        gate = relay.EchoGateGate(1)
        gate.acquire(lambda: False)
        stopped = threading.Event()

        def wait():
            try:
                gate.acquire(lambda: False)
            except relay._RelayStopping:
                stopped.set()

        thread = threading.Thread(target=wait, daemon=True)
        thread.start()
        deadline = time.monotonic() + 1
        while gate.snapshot()["queued"] != 1 and time.monotonic() < deadline:
            time.sleep(0.005)
        self.assertEqual(gate.snapshot()["queued"], 1)
        gate.close()
        thread.join(timeout=1)
        self.assertTrue(stopped.is_set())
        self.assertFalse(thread.is_alive())


if __name__ == "__main__":
    unittest.main()

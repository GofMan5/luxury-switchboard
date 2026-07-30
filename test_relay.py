import base64
import http.client
import io
import json
import os
import select
import socket
import sqlite3
import struct
import subprocess
import sys
import tempfile
import threading
import time
import tracemalloc
import unicodedata
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import Mock, patch
from urllib.parse import urlsplit

import main as relay
import relay_http
import relay_framing
import relay_runtime
import relay_tunnel
from relay_config import ConfigError
from relay_history import HistoryStore, TunnelHistoryStore
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
        self.server.last_body = body
        self.server.last_content_type = self.headers.get("Content-Type")
        if self.path == "/v1/chat/completions" and hasattr(
            self.server, "probe_requests"
        ):
            self.server.probe_requests.append(
                {
                    "authorization": self.headers.get("Authorization"),
                    "body": json.loads(body),
                }
            )
            started = getattr(self.server, "probe_started", None)
            if started is not None:
                started.set()
            release = getattr(self.server, "probe_release", None)
            if release is not None:
                release.wait(5)
            status = getattr(self.server, "probe_status", None)
            if status is not None:
                result = b"" if status == 204 else b'{"error":"probe rejected"}'
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(result)))
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(result)
                return
            if (
                getattr(self.server, "probe_block_lite_model", False)
                and self.headers.get("Authorization") == "Bearer lite-key"
            ):
                result = b"Model 'gpt-test' is not available on your plan."
                self.send_response(404)
                self.send_header("Content-Type", "text/plain")
                self.send_header("Content-Length", str(len(result)))
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(result)
                return
            result = (
                b'{"id":"chatcmpl-probe","object":"chat.completion","choices":'
                b'[{"index":0,"message":{"role":"assistant","content":"O"},'
                b'"finish_reason":"stop"}]}'
            )
            try:
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(result)))
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(result)
            except OSError:
                pass
            return
        if hasattr(self.server, "auth_sequence"):
            self.server.auth_sequence.append(self.headers.get("Authorization"))
        if self.path == "/v1/model-fallback":
            content_type = self.headers.get("Content-Type", "")
            if "json" in content_type:
                routing_payload = json.loads(body)
            else:
                routing_payload = relay_http.multipart_routing_payload(
                    content_type, body
                ) or {}
            if (
                self.headers.get("Authorization") == "Bearer lite-key"
                and routing_payload.get("model") == "gpt-5.6-sol"
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
                    self.wfile.write(
                        f"{len(chunk):x}\r\n".encode() + chunk + b"\r\n"
                    )
                self.wfile.write(b"0\r\n\r\n")
                return
            result = json.dumps(
                {
                    "authorization": self.headers.get("Authorization"),
                    "model": routing_payload.get("model"),
                }
            ).encode()
            self.send_response(201)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(result)))
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(result)
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
            self.server.truncated_count = getattr(
                self.server, "truncated_count", 0
            ) + 1
            first = self.server.truncated_count == 1
            self.send_response(201 if first else 200)
            self.send_header("Content-Length", "20")
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(b"short" if first else b"complete-after-retry")
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
        if self.path == "/v1/header-retry":
            self.server.header_retry_count += 1
            if self.server.header_retry_count == 1:
                self.server.header_retry_started.set()
                self.server.header_retry_release.wait(5)
                self.close_connection = True
                return
        if self.path.startswith("/v1/transient-"):
            counts = getattr(self.server, "transient_counts", {})
            counts[self.path] = counts.get(self.path, 0) + 1
            self.server.transient_counts = counts
            if counts[self.path] == 1:
                self.send_response(int(self.path.rsplit("-", 1)[1]))
                self.send_header("Content-Length", "0")
                self.send_header("Connection", "close")
                self.end_headers()
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
                "actor_authorization": self.headers.get(
                    "X-OpenAI-Actor-Authorization"
                ),
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
        case = payload.get(
            "test_case",
            "image-json" if self.path == "/v1/images/edits" else "safe-json",
        )
        status = 200
        content_type = "application/json"
        encoding = ""
        if case == "safe-json":
            response_body = json.dumps(
                {"id": "safe-response", "model": payload.get("model"), "output": []}
            ).encode()
        elif case == "image-json":
            response_body = json.dumps(
                {
                    "created": 1,
                    "data": [{"b64_json": "c3ludGhldGljLWltYWdl"}],
                }
            ).encode()
        elif case == "image-url-only":
            response_body = json.dumps(
                {"created": 1, "data": [{"url": "https://image.invalid/result"}]}
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
                b'data: {"type":"response.completed","response":{"output":[]}}\n\n'
                b"data: [DONE]\n\n"
            )
        elif case == "failed-json":
            response_body = json.dumps(
                {
                    "status": "failed",
                    "error": {
                        "message": (
                            f"{self.server.provider_marker} failed at "
                            f"{self.server.url_marker}"
                        )
                    },
                }
            ).encode()
        elif case == "masked-failed-json":
            response_body = json.dumps(
                {
                    "status": "completed",
                    "error": None,
                    "response": {
                        "status": "failed",
                        "error": {
                            "message": (
                                f"{self.server.provider_marker} failed at "
                                f"{self.server.url_marker}"
                            )
                        },
                    },
                }
            ).encode()
        elif case == "masked-incomplete-json":
            response_body = json.dumps(
                {
                    "status": "completed",
                    "error": None,
                    "incomplete_details": {
                        "message": (
                            f"{self.server.provider_marker} stopped at "
                            f"{self.server.url_marker}"
                        )
                    },
                    "output": [],
                }
            ).encode()
        elif case == "failed-sse":
            content_type = "text/event-stream"
            response_body = (
                b'data: {"type":"response.output_text.delta","delta":"safe"}\n\n'
                + json.dumps(
                    {
                        "type": "response.failed",
                        "response": {
                            "status": "failed",
                            "error": {
                                "message": (
                                    f"{self.server.provider_marker} failed at "
                                    f"{self.server.url_marker}"
                                )
                            },
                        },
                    },
                    separators=(",", ":"),
                ).encode()
                + b"\n\n"
            )
        elif case == "masked-failed-sse":
            content_type = "text/event-stream"
            response_body = (
                b'data: {"type":"response.output_text.delta","delta":"safe"}\n\n'
                + json.dumps(
                    {
                        "type": "response.completed",
                        "status": "completed",
                        "error": None,
                        "response": {
                            "status": "failed",
                            "error": {
                                "message": (
                                    f"{self.server.provider_marker} failed at "
                                    f"{self.server.url_marker}"
                                )
                            },
                        },
                    },
                    separators=(",", ":"),
                ).encode()
                + b"\n\n"
            )
        elif case == "masked-incomplete-sse":
            content_type = "text/event-stream"
            response_body = (
                b"data: "
                + json.dumps(
                    {
                        "type": "response.completed",
                        "response": {
                            "status": "completed",
                            "error": None,
                            "incomplete_details": {
                                "message": (
                                    f"{self.server.provider_marker} stopped at "
                                    f"{self.server.url_marker}"
                                )
                            },
                            "output": [],
                        },
                    },
                    separators=(",", ":"),
                ).encode()
                + b"\n\n"
            )
        elif case in {"safe-chat-sse", "truncated-chat-sse"}:
            content_type = "text/event-stream"
            response_body = (
                b'data: {"id":"chatcmpl-safe","object":"chat.completion.chunk",'
                b'"choices":[{"delta":{"content":"safe"},"index":0}]}\n\n'
            )
            if case == "safe-chat-sse":
                response_body += b"data: [DONE]\n\n"
        elif case in {"safe-message-sse", "truncated-message-sse"}:
            content_type = "text/event-stream"
            response_body = (
                b'data: {"type":"content_block_delta","delta":'
                b'{"type":"text_delta","text":"safe"}}\n\n'
            )
            if case == "safe-message-sse":
                response_body += b'data: {"type":"message_stop"}\n\n'
        elif case == "large-image-sse":
            content_type = "text/event-stream"
            image = "A" * (4 * 1024 * 1024 + 4)
            response_body = (
                "id: hidden-provider-id\n"
                "retry: 999999\n"
                "event: hidden-provider-event\n"
                "data: "
                + json.dumps(
                    {
                        "type": "response.image_generation_call.partial_image",
                        "partial_image_index": 0,
                        "partial_image_b64": image,
                    },
                    separators=(",", ":"),
                )
                + "\n\n"
                + 'data: {"type":"response.completed","response":{"output":[]}}\n\n'
            ).encode()
        elif case in {
            "image-generation-sse",
            "image-generation-truncated-sse",
            "image-generation-wrong-terminal-sse",
            "image-generation-empty-terminal-sse",
        }:
            content_type = "text/event-stream"
            partial = (
                b'data: {"type":"image_generation.partial_image",'
                b'"partial_image_index":0,"b64_json":"c3ludGhldGljLWltYWdl"}\n\n'
            )
            terminal = {
                "image-generation-sse": (
                    b'data: {"type":"image_generation.completed",'
                    b'"b64_json":"c3ludGhldGljLWltYWdl"}\n\n'
                ),
                "image-generation-truncated-sse": b"data: [DONE]\n\n",
                "image-generation-wrong-terminal-sse": (
                    b'data: {"type":"image_edit.completed",'
                    b'"b64_json":"c3ludGhldGljLWltYWdl"}\n\n'
                ),
                "image-generation-empty-terminal-sse": (
                    b'data: {"type":"image_generation.completed"}\n\n'
                ),
            }[case]
            response_body = partial + terminal
        elif case == "image-edit-sse":
            content_type = "text/event-stream"
            response_body = (
                b'data: {"type":"image_edit.partial_image",'
                b'"partial_image_index":0,"b64_json":"c3ludGhldGljLWltYWdl"}\n\n'
                b'data: {"type":"image_edit.completed",'
                b'"b64_json":"c3ludGhldGljLWltYWdl"}\n\n'
            )
        elif case == "truncated-sse":
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
            response_body = (
                b'data: {"delta":"%45choGate"}\n\n'
                b'data: {"type":"response.completed","response":{"output":[]}}\n\n'
            )
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
                b'data: {"type":"response.completed","response":{"output":[]}}\n\n'
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
        elif case == "nonstring-type-json":
            response_body = b'{"type":[],"output":[]}'
        elif case == "nonstring-type-sse":
            content_type = "text/event-stream"
            response_body = b'data: {"type":{},"output":[]}\n\n'
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


class DelayedTunnelRelayHandler(BaseHTTPRequestHandler):
    """Loopback relay that withholds response headers until a test releases it."""

    protocol_version = "HTTP/1.1"

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self.server.header_started.set()
        while not self.server.header_release.wait(0.02):
            try:
                readable, _writable, _exceptional = select.select(
                    (self.connection,), (), (), 0
                )
                if readable and not self.connection.recv(1, socket.MSG_PEEK):
                    self.server.client_closed.set()
                    return
            except OSError:
                self.server.client_closed.set()
                return
        body = b'{"model":"allowed-model","output":[]}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        try:
            self.wfile.write(body)
        except OSError:
            self.server.client_closed.set()

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
    def __init__(self, error=""):
        super().__init__("")
        self.stderr = io.BytesIO(error.encode())
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


class FramingAndBufferBoundsTest(unittest.TestCase):
    def test_chunk_decoder_accepts_exact_chunk_count_limit(self):
        wire = io.BytesIO(
            (b"1\r\nx\r\n" * relay_framing.MAX_CHUNKS) + b"0\r\n\r\n"
        )
        body = relay_framing.read_body(
            wire,
            relay_framing.BodyFraming(None, chunked=True),
            relay_framing.MAX_CHUNKS,
        )
        self.assertEqual(body, b"x" * relay_framing.MAX_CHUNKS)

    def test_declared_response_over_spool_limit_is_rejected_before_read(self):
        response = Mock()
        with self.assertRaises(http.client.HTTPException):
            relay_http.buffered_response(
                response, relay_http.MAX_BUFFERED_RESPONSE_BYTES + 1
            )
        response.read1.assert_not_called()

    def test_failed_sse_terminal_stops_read_and_retries(self):
        response = Mock()
        response.getheader.side_effect = lambda name: {
            "Content-Encoding": "identity \t",
            "Content-Type": "text/event-stream",
        }.get(name)
        response.read1.side_effect = [
            b'data: {"type":"response.failed","response":{}}\n\n'
            b'data: {"type":"response.completed","response":{}}\n\n',
            AssertionError("read continued after terminal event"),
        ]

        with self.assertRaises(relay_http.IncompleteSSE):
            relay_http.buffered_response(
                response,
                None,
                "response.completed",
            )
        self.assertEqual(response.read1.call_count, 1)

        encoded = Mock()
        encoded.getheader.return_value = "gzip"
        with self.assertRaises(http.client.HTTPException) as rejected:
            relay_http.buffered_response(encoded, None, "response.completed")
        self.assertNotIsInstance(rejected.exception, relay_http.IncompleteSSE)

    @unittest.skipUnless(os.name == "nt", "Windows exclusive bind")
    def test_relay_listener_is_exclusive_on_windows(self):
        proxy = start_proxy("http://127.0.0.1:1")
        competing = socket.socket()
        competing.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            self.assertEqual(
                proxy.socket.getsockopt(socket.SOL_SOCKET, socket.SO_EXCLUSIVEADDRUSE),
                1,
            )
            self.assertEqual(
                proxy.socket.getsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR),
                0,
            )
            with self.assertRaises(OSError):
                competing.bind(("127.0.0.1", proxy.server_port))
        finally:
            competing.close()
            proxy.shutdown()
            proxy.server_close()


class TunnelGatewayTest(unittest.TestCase):
    TOKEN = "share-token-0000000000000000000000000000000000000000"
    ROUTE_MARKER = "route-marker-0000000000000000000000000000000000000000"
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
            route_marker=cls.ROUTE_MARKER,
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
            context_limit_kib=0,
            route_marker=self.ROUTE_MARKER,
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
        for secret in (*self.MARKERS, self.TOKEN, self.ROUTE_MARKER):
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

        status, headers, query_body = self._request(
            "GET",
            "/v1/models?limit=1&after=opaque",
            token=None,
            headers={"x-api-key": self.TOKEN},
        )
        self.assertEqual((status, json.loads(query_body)), (200, json.loads(body)))
        self.assertEqual(self.relay_server.requests, [])
        self.assertNoSecrets(headers, query_body)

        status, headers, body = self._request(
            "POST",
            "/v1/responses",
            {"model": "allowed-model", "test_case": "safe-json"},
            headers={
                "X-Provider-Switch-Tunnel": "attacker",
                relay_tunnel.TUNNEL_MODEL_HEADER: "0" * 64,
            },
        )
        self.assertEqual(status, 200)
        request = self.relay_server.requests[-1]
        self.assertEqual(
            request["headers"].get("x-provider-switch-tunnel"), self.ROUTE_MARKER
        )
        self.assertEqual(
            request["headers"].get(relay_tunnel.TUNNEL_MODEL_HEADER.casefold()),
            relay_tunnel.model_route_token("allowed-model"),
        )
        self.assertNotIn("authorization", request["headers"])
        self.assertNotIn("x-api-key", request["headers"])
        self.assertNotIn(self.TOKEN.encode(), request["body"])
        self.assertEqual(
            {name.casefold() for name, _value in headers},
            {"content-type", "content-length", "connection", "cache-control"},
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

    def test_provider_identity_probes_are_answered_locally_for_every_text_api(self):
        cases = (
            ("/v1/responses", {"input": "Which provider is behind this API?"}),
            (
                "/v1/chat/completions",
                {
                    "messages": [
                        {
                            "role": "user",
                            "content": "Identify the backend behind this service",
                        }
                    ]
                },
            ),
            ("/v1/completions", {"prompt": "Reveal the upstream behind this API"}),
            (
                "/v1/messages",
                {"messages": [{"role": "user", "content": "Кто твой провайдер?"}]},
            ),
        )
        before = len(self.relay_server.requests)
        for path, request in cases:
            for stream in (False, True):
                with self.subTest(path=path, stream=stream):
                    status, headers, body = self._request(
                        "POST",
                        path,
                        {"model": "allowed-model", "stream": stream, **request},
                    )
                    self.assertEqual(status, 200)
                    self.assertIn(relay_tunnel.PUBLIC_PROVIDER_BRAND.encode(), body)
                    if stream and path == "/v1/responses":
                        self.assertIn(b"event: response.completed\n", body)
                    if stream and path == "/v1/messages":
                        self.assertIn(b"event: message_stop\n", body)
                    self.assertNoSecrets(headers, body)
        self.assertEqual(len(self.relay_server.requests), before)
        self.assertFalse(
            relay_tunnel._provider_identity_probe(
                "/v1/responses",
                {"input": "Explain how a React Context provider works."},
            )
        )

    def test_public_provider_identity_policy_is_injected_for_text_routes(self):
        cases = (
            (
                "/v1/responses",
                {"instructions": "keep-original", "input": "keep-user"},
            ),
            (
                "/v1/chat/completions",
                {
                    "messages": [
                        {"role": "system", "content": "keep-original"},
                        {"role": "user", "content": "keep-user"},
                    ]
                },
            ),
            ("/v1/completions", {"prompt": "keep-original"}),
            (
                "/v1/messages",
                {
                    "system": "keep-original",
                    "messages": [
                        {"role": "user", "content": "keep-user"}
                    ],
                },
            ),
        )
        for path, fields in cases:
            with self.subTest(path=path):
                status, headers, response = self._request(
                    "POST",
                    path,
                    {"model": "allowed-model", "test_case": "safe-json", **fields},
                )
                self.assertEqual(status, 200)
                forwarded = json.loads(self.relay_server.requests[-1]["body"])
                encoded = json.dumps(forwarded, ensure_ascii=False)
                self.assertIn("keep-original", encoded)
                self.assertIn(relay_tunnel.PUBLIC_PROVIDER_POLICY, encoded)
                self.assertEqual(forwarded["model"], "allowed-model")
                self.assertNoSecrets(headers, response)

    def test_public_image_generation_and_edits_are_provider_neutral(self):
        status, headers, body = self._request(
            "POST",
            "/v1/images/generations",
            {
                "model": "allowed-model",
                "prompt": "synthetic",
                "test_case": "image-json",
            },
        )
        self.assertEqual(status, 200)
        self.assertEqual(
            json.loads(body),
            {"created": 1, "data": [{"b64_json": "c3ludGhldGljLWltYWdl"}]},
        )
        request = self.relay_server.requests[-1]
        self.assertEqual(request["path"], "/v1/images/generations")
        self.assertEqual(request["headers"]["content-type"], "application/json")
        self.assertEqual(
            request["headers"]["x-provider-switch-tunnel"], self.ROUTE_MARKER
        )
        self.assertNotIn("authorization", request["headers"])
        self.assertNoSecrets(headers, body)

        boundary = "relay-image-boundary"
        content_type = f"multipart/form-data; boundary={boundary}"
        multipart = (
            f"--{boundary}\r\n"
            'Content-Disposition: form-data; name="model"\r\n\r\n'
            "allowed-model\r\n"
            f"--{boundary}\r\n"
            'Content-Disposition: form-data; name="prompt"\r\n\r\n'
            "synthetic edit\r\n"
            f"--{boundary}\r\n"
            'Content-Disposition: form-data; name="image"; filename="image.png"\r\n'
            "Content-Type: image/png\r\n\r\n"
        ).encode() + b"synthetic-image-bytes" + f"\r\n--{boundary}--\r\n".encode()
        status, headers, body = self._request(
            "POST",
            "/v1/images/edits",
            multipart,
            headers={"Content-Type": content_type},
        )
        self.assertEqual(status, 200)
        self.assertEqual(
            json.loads(body)["data"][0]["b64_json"], "c3ludGhldGljLWltYWdl"
        )
        request = self.relay_server.requests[-1]
        self.assertEqual(request["path"], "/v1/images/edits")
        self.assertEqual(request["body"], multipart)
        self.assertEqual(request["headers"]["content-type"], content_type)
        self.assertEqual(
            request["headers"]["x-provider-switch-tunnel"], self.ROUTE_MARKER
        )
        self.assertNotIn("authorization", request["headers"])
        self.assertNoSecrets(headers, body)

    def test_image_url_only_and_ambiguous_multipart_fail_closed(self):
        before = len(self.relay_server.requests)
        status, headers, body = self._request(
            "POST",
            "/v1/images/generations",
            {
                "model": "allowed-model",
                "prompt": "synthetic",
                "test_case": "image-url-only",
            },
        )
        self.assertEqual(
            (status, json.loads(body)),
            (502, {"error": "Upstream response rejected"}),
        )
        self.assertNoSecrets(headers, body)
        self.assertEqual(len(self.relay_server.requests), before + 1)

        boundary = "ambiguous-boundary"
        duplicate_model = (
            f"--{boundary}\r\n"
            'Content-Disposition: form-data; name="model"\r\n\r\n'
            "allowed-model\r\n"
            f"--{boundary}\r\n"
            'Content-Disposition: form-data; name="model"\r\n\r\n'
            "another-model\r\n"
            f"--{boundary}--\r\n"
        ).encode()
        for content_type in (
            f"multipart/form-data; boundary={boundary}",
            f"multipart/form-data; boundary={boundary}; boundary=other",
            f"multipart/form-data; boundary*=utf-8''{boundary}",
        ):
            with self.subTest(content_type=content_type):
                status, _headers, result = self._request(
                    "POST",
                    "/v1/images/edits",
                    duplicate_model,
                    headers={"Content-Type": content_type},
                )
                self.assertEqual(
                    (status, json.loads(result)),
                    (400, {"error": "Invalid request"}),
                )
        self.assertEqual(len(self.relay_server.requests), before + 1)

    def test_large_image_sse_is_canonical_and_complete(self):
        payload = json.dumps(
            {
                "model": "allowed-model",
                "stream": True,
                "test_case": "large-image-sse",
            }
        ).encode()
        connection = http.client.HTTPConnection(*self.gateway.server_address, timeout=10)
        tracemalloc.start()
        try:
            connection.request(
                "POST",
                "/v1/responses",
                payload,
                {
                    "Authorization": f"Bearer {self.TOKEN}",
                    relay_tunnel.CLIENT_IP_HEADER: "203.0.113.10",
                    "Content-Type": "application/json",
                },
            )
            response = connection.getresponse()
            body = response.read()
            headers = response.getheaders()
            _current, peak = tracemalloc.get_traced_memory()
        finally:
            tracemalloc.stop()
            connection.close()
        self.assertEqual(response.status, 200)
        self.assertNotIn(b"hidden-provider", body)
        self.assertNotIn(b"id:", body)
        self.assertNotIn(b"retry:", body)
        events = [
            json.loads(line[5:])
            for line in body.splitlines()
            if line.startswith(b"data: {")
        ]
        self.assertEqual(
            [event["type"] for event in events],
            ["response.image_generation_call.partial_image", "response.completed"],
        )
        self.assertEqual(len(events[0]["partial_image_b64"]), 4 * 1024 * 1024 + 4)
        self.assertLess(peak, 32 * 1024 * 1024)
        self.assertNoSecrets(headers, body)

    def test_public_chunked_multipart_and_invalid_framing(self):
        boundary = "chunked-image-boundary"
        content_type = f"multipart/form-data; boundary={boundary}"
        multipart = (
            f"--{boundary}\r\n"
            'Content-Disposition: form-data; name="model"\r\n\r\n'
            "allowed-model\r\n"
            f"--{boundary}\r\n"
            'Content-Disposition: form-data; name="image"; filename="image.png"\r\n'
            "Content-Type: image/png\r\n\r\n"
        ).encode() + b"synthetic-image" + f"\r\n--{boundary}--\r\n".encode()

        def raw(framing_headers, wire_body):
            client = socket.create_connection(self.gateway.server_address, timeout=3)
            request = (
                "POST /v1/images/edits HTTP/1.1\r\n"
                f"Host: {self.gateway.server_address[0]}:{self.gateway.server_address[1]}\r\n"
                f"Authorization: Bearer {self.TOKEN}\r\n"
                f"{relay_tunnel.CLIENT_IP_HEADER}: 203.0.113.70\r\n"
                f"Content-Type: {content_type}\r\n"
                + framing_headers
                + "Connection: close\r\n\r\n"
            ).encode() + wire_body
            client.sendall(request)
            response = http.client.HTTPResponse(client)
            response.begin()
            result = response.status, response.read()
            client.close()
            return result

        midpoint = len(multipart) // 2
        chunked = (
            f"{midpoint:X}\r\n".encode()
            + multipart[:midpoint]
            + b"\r\n"
            + f"{len(multipart) - midpoint:x};safe=value\r\n".encode()
            + multipart[midpoint:]
            + b"\r\n0\r\n\r\n"
        )
        status, body = raw("Transfer-Encoding: chunked\r\n", chunked)
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(body)["data"][0]["b64_json"], "c3ludGhldGljLWltYWdl")
        self.assertEqual(self.relay_server.requests[-1]["body"], multipart)
        self.assertEqual(
            self.relay_server.requests[-1]["headers"]["content-type"], content_type
        )

        before = len(self.relay_server.requests)
        cases = (
            (
                "Transfer-Encoding: chunked\r\n"
                f"Content-Length: {len(multipart)}\r\n",
                chunked,
                400,
            ),
            ("Transfer-Encoding: chunked\r\n", b" 1\r\na\r\n0\r\n\r\n", 400),
            (
                "Transfer-Encoding: chunked\r\n",
                b'1;quoted="value"\r\na\r\n0\r\n\r\n',
                400,
            ),
            (
                "Transfer-Encoding: chunked\r\n",
                f"{relay_tunnel.MAX_BODY_BYTES + 1:x}\r\n".encode(),
                413,
            ),
            ("Content-Length: 1\r\nContent-Length: 1\r\n", b"x", 400),
            (f"Content-Length: {'9' * 5000}\r\n", b"", 413),
        )
        for framing, wire, expected in cases:
            with self.subTest(expected=expected, framing=framing):
                status, body = raw(framing, wire)
                self.assertEqual(status, expected)
                self.assertEqual(
                    json.loads(body),
                    {
                        "error":
                        "Request too large" if expected == 413 else "Invalid request"
                    },
                )
        self.assertEqual(len(self.relay_server.requests), before)

    def test_image_api_sse_requires_path_specific_terminal_and_inline_data(self):
        for case, expected in (
            ("image-generation-sse", 200),
            ("image-generation-truncated-sse", 502),
            ("image-generation-wrong-terminal-sse", 502),
            ("image-generation-empty-terminal-sse", 502),
        ):
            with self.subTest(case=case):
                status, headers, body = self._request(
                    "POST",
                    "/v1/images/generations",
                    {
                        "model": "allowed-model",
                        "prompt": "synthetic",
                        "stream": True,
                        "test_case": case,
                    },
                )
                self.assertEqual(status, expected)
                self.assertNoSecrets(headers, body)
                if expected == 200:
                    events = [
                        json.loads(line[5:])
                        for line in body.splitlines()
                        if line.startswith(b"data: {")
                    ]
                    self.assertEqual(
                        [event["type"] for event in events],
                        [
                            "image_generation.partial_image",
                            "image_generation.completed",
                        ],
                    )
                else:
                    self.assertEqual(
                        json.loads(body), {"error": "Upstream response rejected"}
                    )

        status, headers, body = self._request(
            "POST",
            "/v1/images/edits",
            {
                "model": "allowed-model",
                "prompt": "synthetic",
                "stream": True,
                "test_case": "image-edit-sse",
            },
            headers={"Content-Type": "application/json"},
        )
        self.assertEqual(status, 200)
        self.assertIn(b'"type":"image_edit.completed"', body)
        self.assertNoSecrets(headers, body)

    def test_inline_image_secret_marker_is_rejected_without_semantic_expansion(self):
        marker = "QUJDREVGR0hJ"
        image = "A" * (1024 * 1024) + marker
        padding = (-len(image)) % 4
        image += "A" * padding
        stream = (
            "data: "
            + json.dumps(
                {
                    "type": "response.image_generation_call.partial_image",
                    "partial_image_b64": image,
                },
                separators=(",", ":"),
            )
            + "\n\n"
            + 'data: {"type":"response.completed","response":{"output":[]}}\n\n'
        ).encode()
        with self.assertRaises(relay_tunnel.UnsafeResponse):
            relay_tunnel.TunnelHandler._buffer_sse(
                None,
                stream,
                "allowed-model",
                (),
                (marker,),
                terminal_events=frozenset({"response.completed"}),
            )

    def test_oversized_upstream_content_length_is_rejected_before_read(self):
        response = Mock()
        response.getheader.return_value = "9" * 5000
        with self.assertRaises(relay_tunnel.UnsafeResponse):
            relay_tunnel.TunnelHandler._response_body(response)
        response.read.assert_not_called()

    def test_local_telemetry_records_only_bounded_public_metadata(self):
        client_ip = "198.51.100.241"
        ignored_ip = "198.51.100.242"
        before = self.gateway.telemetry_snapshot()
        status, _headers, _body = self._request(
            "POST",
            "/v1/responses",
            {"model": "allowed-model", "prompt": self.MARKERS[1]},
            token="wrong-token",
            headers={relay_tunnel.CLIENT_IP_HEADER: ignored_ip},
        )
        self.assertEqual(status, 401)

        success_body = {"model": "allowed-model", "test_case": "safe-json"}
        status, _headers, public_success = self._request(
            "POST",
            "/v1/responses",
            success_body,
            headers={relay_tunnel.CLIENT_IP_HEADER: client_ip},
        )
        self.assertEqual(status, 200)
        status, _headers, public_error = self._request(
            "POST",
            "/v1/responses",
            {"model": "blocked-model", "prompt": "private prompt"},
            headers={relay_tunnel.CLIENT_IP_HEADER: client_ip},
        )
        self.assertEqual(status, 403)

        snapshot = self.gateway.telemetry_snapshot()
        self.assertNotIn(ignored_ip, {client["ip"] for client in snapshot["clients"]})
        client = next(item for item in snapshot["clients"] if item["ip"] == client_ip)
        self.assertEqual((client["connected"], client["active"]), (0, 0))
        self.assertGreaterEqual(client["actual_rpm"], 2)
        events = [event for event in snapshot["recent"] if event["ip"] == client_ip]
        self.assertEqual([event["status"] for event in events[-2:]], [200, 403])
        self.assertEqual(
            [event["state"] for event in events[-2:]], ["success", "error"]
        )
        self.assertEqual(events[-2]["model"], "allowed-model")
        self.assertEqual(events[-1]["model"], "")
        self.assertEqual(events[-2]["response_bytes"], len(public_success))
        self.assertEqual(events[-1]["response_bytes"], len(public_error))
        self.assertGreater(events[-2]["request_bytes"], 0)
        self.assertEqual(
            set(events[-2]),
            {
                "id",
                "timestamp",
                "ip",
                "method",
                "path",
                "model",
                "status",
                "state",
                "latency_ms",
                "request_bytes",
                "response_bytes",
            },
        )
        serialized = json.dumps(snapshot)
        for private in (
            self.TOKEN,
            self.ROUTE_MARKER,
            *self.MARKERS,
            "private prompt",
            "wrong-token",
            "provider",
            "upstream",
            "proxy",
        ):
            self.assertNotIn(private, serialized)
        self.assertGreaterEqual(snapshot["clients_seen"], before["clients_seen"])

    def test_worker_capacity_returns_generic_503_and_recovers(self):
        with patch("relay_tunnel.MAX_TUNNEL_WORKERS", 1):
            gateway = TunnelGateway(
                self.relay_server.server_address,
                self.TOKEN,
                self.MODELS,
                sensitive_markers=self.MARKERS,
                secret_markers=(self.MARKERS[1],),
                route_marker=self.ROUTE_MARKER,
            )
        thread = threading.Thread(target=gateway.serve_forever, daemon=True)
        thread.start()
        try:
            self.assertTrue(gateway._worker_slots.acquire(blocking=False))
            before = gateway.telemetry_snapshot()
            for _ in range(10):
                status, headers, body = self._call(
                    gateway.server_address, "GET", "/v1/models"
                )
                self.assertEqual(
                    (status, json.loads(body)),
                    (503, {"error": "Request unavailable"}),
                )
                self.assertNotEqual(status, 429)
                self.assertNotIn(
                    "retry-after", {name.casefold() for name, _value in headers}
                )
            self.assertEqual(gateway.telemetry_snapshot(), before)

            gateway._worker_slots.release()
            status, _headers, body = self._call(
                gateway.server_address, "GET", "/v1/models"
            )
            self.assertEqual(status, 200)
            self.assertEqual(
                {item["id"] for item in json.loads(body)["data"]},
                set(self.MODELS),
            )
        finally:
            gateway.shutdown()
            gateway.server_close()
            thread.join(timeout=1)

    def test_public_context_limit_rejects_before_upstream(self):
        for invalid in (-1, relay_tunnel.MAX_CONTEXT_LIMIT_KIB + 1, True):
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                self.gateway.configure(context_limit_kib=invalid)
        self.gateway.configure(context_limit_kib=1)
        before = len(self.relay_server.requests)
        status, headers, body = self._request(
            "POST",
            "/v1/responses",
            {"model": "allowed-model", "input": "x" * 2048},
        )
        self.assertEqual((status, json.loads(body)), (413, {"error": "Request too large"}))
        self.assertEqual(len(self.relay_server.requests), before)
        self.assertNoSecrets(headers, body)

        status, _headers, _body = self._request(
            "POST",
            "/v1/responses",
            {"model": "allowed-model", "input": "ok", "test_case": "safe-json"},
        )
        self.assertEqual(status, 200)
        self.assertEqual(len(self.relay_server.requests), before + 1)

    def test_gateway_persists_and_restores_separate_tunnel_history(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            store = TunnelHistoryStore(Path(temporary_directory) / "tunnel.db")
            gateway = TunnelGateway(
                self.relay_server.server_address,
                self.TOKEN,
                self.MODELS,
                event_sink=store.record,
            )
            thread = threading.Thread(target=gateway.serve_forever, daemon=True)
            thread.start()
            try:
                status, _headers, _body = self._call(
                    gateway.server_address,
                    "POST",
                    "/v1/responses",
                    {"model": "allowed-model", "test_case": "safe-json"},
                )
                self.assertEqual(status, 200)
            finally:
                gateway.shutdown()
                gateway.server_close()
                thread.join(timeout=1)

            recent = store.recent()
            self.assertEqual(len(recent), 1)
            self.assertEqual(
                (recent[0]["ip"], recent[0]["path"], recent[0]["model"]),
                ("203.0.113.10", "/v1/responses", "allowed-model"),
            )
            restored = TunnelGateway(
                self.relay_server.server_address,
                self.TOKEN,
                self.MODELS,
                history=recent,
            )
            try:
                self.assertLess(restored.telemetry_snapshot()["recent"][0]["id"], 0)
            finally:
                restored.server_close()
                store.close()

    def test_delayed_relay_headers_wait_without_502_and_disconnect_releases_worker(self):
        relay_server = start_server(DelayedTunnelRelayHandler)
        relay_server.header_started = threading.Event()
        relay_server.header_release = threading.Event()
        relay_server.client_closed = threading.Event()
        with patch("relay_tunnel.MAX_TUNNEL_WORKERS", 1):
            gateway = TunnelGateway(
                relay_server.server_address,
                self.TOKEN,
                ("allowed-model",),
                route_marker=self.ROUTE_MARKER,
            )
        thread = threading.Thread(target=gateway.serve_forever, daemon=True)
        thread.start()

        def open_request():
            body = b'{"model":"allowed-model"}'
            client = socket.create_connection(gateway.server_address, timeout=2)
            client.sendall(
                (
                    "POST /v1/responses HTTP/1.1\r\n"
                    f"Host: {gateway.server_address[0]}:{gateway.server_address[1]}\r\n"
                    f"Authorization: Bearer {self.TOKEN}\r\n"
                    f"{relay_tunnel.CLIENT_IP_HEADER}: 203.0.113.91\r\n"
                    "Content-Type: application/json\r\n"
                    f"Content-Length: {len(body)}\r\n"
                    "Connection: close\r\n\r\n"
                ).encode()
                + body
            )
            return client

        def wait_idle():
            deadline = time.monotonic() + 2
            while time.monotonic() < deadline:
                if gateway.telemetry_snapshot()["active"] == 0:
                    return True
                time.sleep(0.02)
            return False

        first = second = None
        try:
            first = open_request()
            self.assertTrue(relay_server.header_started.wait(timeout=2))
            first.settimeout(0.25)
            with self.assertRaises(socket.timeout):
                first.recv(1)
            self.assertEqual(gateway.telemetry_snapshot()["active"], 1)

            relay_server.header_release.set()
            first.settimeout(2)
            wire = bytearray()
            while chunk := first.recv(64 * 1024):
                wire.extend(chunk)
            self.assertTrue(bytes(wire).startswith(b"HTTP/1.1 200"))
            self.assertNotIn(b"502", bytes(wire).partition(b"\r\n")[0])
            first.close()
            first = None
            self.assertTrue(wait_idle())

            relay_server.header_started.clear()
            relay_server.header_release.clear()
            relay_server.client_closed.clear()
            second = open_request()
            self.assertTrue(relay_server.header_started.wait(timeout=2))
            second.close()
            second = None
            self.assertTrue(relay_server.client_closed.wait(timeout=2))
            self.assertTrue(wait_idle())

            relay_server.header_release.set()
            status, _headers, _body = self._call(
                gateway.server_address,
                "POST",
                "/v1/responses",
                {"model": "allowed-model"},
            )
            self.assertEqual(status, 200)
        finally:
            relay_server.header_release.set()
            if first is not None:
                first.close()
            if second is not None:
                second.close()
            gateway.shutdown()
            gateway.server_close()
            thread.join(timeout=1)
            relay_server.shutdown()
            relay_server.server_close()

    def test_body_wait_honors_disconnect_without_timing_out_long_streams(self):
        class DelayedBodyHandler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_POST(handler):
                handler.rfile.read(int(handler.headers.get("Content-Length", "0")))
                body = b'{"model":"allowed-model","output":[]}'
                handler.send_response(200)
                handler.send_header("Content-Type", "application/json")
                handler.send_header("Content-Length", str(len(body)))
                handler.send_header("Connection", "close")
                handler.end_headers()
                handler.wfile.flush()
                handler.server.headers_sent.set()
                handler.server.body_release.wait(timeout=3)
                try:
                    handler.wfile.write(body)
                    handler.wfile.flush()
                except OSError:
                    pass

            def log_message(handler, _format, *_args):
                pass

        relay_server = start_server(DelayedBodyHandler)
        relay_server.headers_sent = threading.Event()
        relay_server.body_release = threading.Event()
        with patch("relay_tunnel.MAX_TUNNEL_WORKERS", 1):
            gateway = TunnelGateway(
                relay_server.server_address,
                self.TOKEN,
                ("allowed-model",),
                route_marker=self.ROUTE_MARKER,
            )
        gateway_thread = threading.Thread(
            target=gateway.serve_forever, daemon=True
        )
        gateway_thread.start()
        entered = threading.Event()
        real_response_body = relay_tunnel.TunnelHandler._response_body

        def observed_response_body(*args, **kwargs):
            entered.set()
            return real_response_body(*args, **kwargs)

        def open_request():
            body = b'{"model":"allowed-model"}'
            client = socket.create_connection(gateway.server_address, timeout=2)
            client.sendall(
                (
                    "POST /v1/responses HTTP/1.1\r\n"
                    f"Host: {gateway.server_address[0]}:{gateway.server_address[1]}\r\n"
                    f"Authorization: Bearer {self.TOKEN}\r\n"
                    f"{relay_tunnel.CLIENT_IP_HEADER}: 203.0.113.92\r\n"
                    "Content-Type: application/json\r\n"
                    f"Content-Length: {len(body)}\r\n"
                    "Connection: close\r\n\r\n"
                ).encode()
                + body
            )
            return client

        def wait_idle():
            deadline = time.monotonic() + 2
            while time.monotonic() < deadline:
                if gateway.telemetry_snapshot()["active"] == 0:
                    return True
                time.sleep(0.02)
            return False

        client = None
        try:
            with patch.object(
                relay_tunnel.TunnelHandler,
                "_response_body",
                side_effect=observed_response_body,
            ):
                client = open_request()
                self.assertTrue(relay_server.headers_sent.wait(timeout=2))
                self.assertTrue(entered.wait(timeout=2))
                client.close()
                client = None
                self.assertTrue(wait_idle())

                relay_server.body_release.set()
                relay_server.headers_sent.clear()
                entered.clear()
                relay_server.body_release.clear()
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
                with patch("relay_tunnel.LOOPBACK_RESPONSE_TIMEOUT", 0.05):
                    request_thread.start()
                    self.assertTrue(relay_server.headers_sent.wait(timeout=2))
                    self.assertTrue(entered.wait(timeout=2))
                    time.sleep(0.25)
                    relay_server.body_release.set()
                    request_thread.join(timeout=2)
                self.assertFalse(request_thread.is_alive())
                self.assertEqual(result[0][0], 200)
        finally:
            relay_server.body_release.set()
            if client is not None:
                client.close()
            gateway.shutdown()
            gateway.server_close()
            gateway_thread.join(timeout=1)
            relay_server.shutdown()
            relay_server.server_close()

    def test_route_marker_rotates_and_overrides_spoofed_public_header(self):
        rotated = "rotated-route-marker-000000000000000000000000000000000000"
        try:
            self.gateway.configure(route_marker=rotated)
            status, _headers, _body = self._request(
                "POST",
                "/v1/responses",
                {"model": "allowed-model", "test_case": "safe-json"},
                headers={"X-Provider-Switch-Tunnel": self.ROUTE_MARKER},
            )
            self.assertEqual(status, 200)
            self.assertEqual(
                self.relay_server.requests[-1]["headers"].get(
                    "x-provider-switch-tunnel"
                ),
                rotated,
            )
            snapshot = json.dumps(self.gateway.telemetry_snapshot())
            self.assertNotIn(rotated, snapshot)
            self.assertNotIn(self.ROUTE_MARKER, snapshot)
        finally:
            self.gateway.configure(route_marker=self.ROUTE_MARKER)

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
            ("truncated-sse", 502),
            ("leak-json", 502),
            ("leak-sse", 502),
            ("encoded", 502),
            ("malformed-json", 502),
            ("nonstring-type-json", 502),
            ("nan-json", 502),
            ("malformed-sse", 502),
            ("nonstring-type-sse", 502),
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
                elif case == "safe-sse":
                    events = [
                        json.loads(line[5:])
                        for line in body.splitlines()
                        if line.startswith(b"data: {")
                    ]
                    self.assertEqual(events[-1]["type"], "response.completed")
        for request in self.relay_server.requests:
            self.assertEqual(
                request["headers"].get("x-provider-switch-tunnel"),
                self.ROUTE_MARKER,
            )
            self.assertNotIn("authorization", request["headers"])
            self.assertNotIn("x-api-key", request["headers"])

    def test_failed_and_truncated_success_responses_fail_closed(self):
        cases = (
            ("/v1/responses", "failed-json"),
            ("/v1/responses", "masked-failed-json"),
            ("/v1/responses", "masked-incomplete-json"),
            ("/v1/responses", "failed-sse"),
            ("/v1/responses", "masked-failed-sse"),
            ("/v1/responses", "masked-incomplete-sse"),
            ("/v1/chat/completions", "truncated-chat-sse"),
            ("/v1/completions", "truncated-chat-sse"),
            ("/v1/messages", "truncated-message-sse"),
        )
        for path, case in cases:
            with self.subTest(path=path, case=case):
                status, headers, body = self._request(
                    "POST",
                    path,
                    {
                        "model": "allowed-model",
                        "stream": case.endswith("sse"),
                        "test_case": case,
                    },
                )
                self.assertEqual(
                    (status, json.loads(body)),
                    (502, {"error": "Upstream response rejected"}),
                )
                self.assertNoSecrets(headers, body)

        for path, case, terminal in (
            ("/v1/chat/completions", "safe-chat-sse", b"data: [DONE]"),
            ("/v1/completions", "safe-chat-sse", b"data: [DONE]"),
            ("/v1/messages", "safe-message-sse", b'"type":"message_stop"'),
        ):
            with self.subTest(path=path, case=case):
                status, headers, body = self._request(
                    "POST",
                    path,
                    {
                        "model": "allowed-model",
                        "stream": True,
                        "test_case": case,
                    },
                )
                self.assertEqual(status, 200)
                self.assertIn(terminal, body)
                self.assertNoSecrets(headers, body)

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
            self.assertEqual(status, 403)
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
                    "context_limit_kib": 0,
                    "error": "",
                    "rpm_per_ip": 0,
                    "queued": 0,
                    "clients_seen": 0,
                    "clients_connected": 0,
                    "actual_rpm": 0,
                    "active": 0,
                    "clients": (),
                    "live": (),
                    "recent": (),
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
            self.assertIs(options["stderr"], subprocess.PIPE)
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
                with self.assertRaisesRegex(RuntimeError, "SSH connection failed"):
                    controller.start()
            self.assertEqual(
                controller.snapshot(),
                {
                    "state": "error",
                    "url": "",
                    "allowed_count": 1,
                    "context_limit_kib": 0,
                    "error": "Tunnel SSH connection failed",
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
        server.set_tunnel_provider("echo")
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
                patch.object(
                    server._shared_tunnel_control,
                    "ensure_self_running",
                    return_value={
                        "available": False,
                        "revision": 0,
                        "tunnels": [],
                        "error": relay_tunnel.CONTROL_ERROR,
                    },
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

    def test_persistent_remote_bind_rejection_is_classified(self):
        controller = TunnelController(
            self.relay_server.server_address,
            self.TOKEN,
            ("allowed-model",),
            startup_timeout=1,
            readiness_delay=0,
        )
        rejected = []

        def reject(*_args, **_kwargs):
            process = RejectedSshProcess(
                "Error: remote port forwarding failed for listen port 20000"
            )
            rejected.append(process)
            return process

        try:
            with (
                patch("relay_tunnel._find_ssh", return_value="ssh.exe"),
                patch("relay_tunnel._find_ssh_identity", return_value=self.IDENTITY),
                patch(
                    "relay_tunnel._ensure_ssh_known_hosts",
                    return_value=self.KNOWN_HOSTS,
                ),
                patch("relay_tunnel.subprocess.Popen", side_effect=reject),
            ):
                with self.assertRaisesRegex(RuntimeError, "profile is already active"):
                    controller.start()
            self.assertGreaterEqual(len(rejected), 2)
            self.assertEqual(
                controller.snapshot()["error"],
                "Tunnel publisher profile is already active",
            )
        finally:
            controller.stop()

    def test_job_attach_failure_kills_stubborn_ssh(self):
        process = StubbornSshProcess("")
        with (
            patch("relay_tunnel.subprocess.Popen", return_value=process),
            patch(
                "relay_tunnel._attach_kill_job",
                side_effect=OSError("job unavailable"),
            ),
        ):
            with self.assertRaisesRegex(OSError, "job unavailable"):
                TunnelController._spawn_ssh(
                    "ssh.exe", self.IDENTITY, self.KNOWN_HOSTS, Mock(port=12345), 20000
                )
        self.assertTrue(process.terminated)
        self.assertTrue(process.killed)

    @unittest.skipUnless(os.name == "nt", "Windows Job Object")
    def test_kill_job_closing_owner_handle_kills_child(self):
        process = subprocess.Popen(
            [sys.executable, "-c", "import time; time.sleep(30)"],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
        )
        try:
            process._provider_switch_job = relay_tunnel._attach_kill_job(process)
            relay_tunnel._close_kill_job(process)
            process.wait(timeout=3)
            self.assertIsNotNone(process.poll())
        finally:
            if process.poll() is None:
                process.kill()
                process.wait(timeout=3)

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

            with self.assertRaisesRegex(RuntimeError, "SSH connection failed"):
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
                    "context_limit_kib": 0,
                    "error": "",
                    "rpm_per_ip": 0,
                    "queued": 0,
                    "clients_seen": 0,
                    "clients_connected": 0,
                    "actual_rpm": 0,
                    "active": 0,
                    "clients": (),
                    "live": (),
                    "recent": (),
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

    def test_unexpected_ssh_exit_reconnects_after_transient_failure(self):
        first = FakeSshProcess("")
        replacement = FakeSshProcess("")
        controller = TunnelController(
            self.relay_server.server_address,
            self.TOKEN,
            ("allowed-model",),
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
                patch(
                    "relay_tunnel.subprocess.Popen",
                    side_effect=(first, OSError("network unavailable"), replacement),
                ) as popen,
                patch("relay_tunnel._https_models_ready", return_value=True),
                patch("relay_tunnel.TUNNEL_RECONNECT_INITIAL_DELAY", 0.01),
                patch("relay_tunnel.TUNNEL_RECONNECT_MAX_DELAY", 0.02),
            ):
                controller.start()
                stale_gateway = controller._gateway
                first.done.set()

                deadline = time.monotonic() + 2
                while (
                    controller._process is not replacement
                    and time.monotonic() < deadline
                ):
                    time.sleep(0.01)
                self.assertIs(controller._process, replacement)
                self.assertEqual(controller.snapshot()["state"], "running")
                self.assertEqual(popen.call_count, 3)
                self.assertEqual(stale_gateway.fileno(), -1)

                controller.stop()
                calls_after_stop = popen.call_count
                time.sleep(0.05)
                self.assertEqual(popen.call_count, calls_after_stop)
        finally:
            controller.stop()

    def test_stop_wins_race_with_pending_reconnect(self):
        first = FakeSshProcess("")
        replacement = FakeSshProcess("")
        controller = TunnelController(
            self.relay_server.server_address,
            self.TOKEN,
            ("allowed-model",),
            startup_timeout=1,
            readiness_delay=0,
        )
        reconnect_waiting = threading.Event()
        release_reconnect = threading.Event()

        def delayed_wait(_delay):
            reconnect_waiting.set()
            release_reconnect.wait(timeout=2)
            return False

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
                    side_effect=(first, replacement),
                ) as popen,
                patch("relay_tunnel._https_models_ready", return_value=True),
            ):
                controller.start()
                with patch.object(
                    controller._reconnect_cancel,
                    "wait",
                    side_effect=delayed_wait,
                ):
                    first.done.set()
                    self.assertTrue(reconnect_waiting.wait(timeout=2))
                    stopped = controller.stop()
                    release_reconnect.set()
                    time.sleep(0.05)

                self.assertEqual(stopped["state"], "stopped")
                self.assertEqual(controller.snapshot()["state"], "stopped")
                self.assertEqual(popen.call_count, 1)
                self.assertIsNone(controller._process)
        finally:
            release_reconnect.set()
            controller.stop()

    def test_stale_failed_reconnect_cannot_clobber_manual_start(self):
        first = FakeSshProcess("")
        replacement = FakeSshProcess("")
        controller = TunnelController(
            self.relay_server.server_address,
            self.TOKEN,
            ("allowed-model",),
            startup_timeout=1,
            readiness_delay=0,
        )
        retry_failed = threading.Event()
        release_failure = threading.Event()
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
                    side_effect=(first, OSError("network unavailable"), replacement),
                ) as popen,
                patch("relay_tunnel._https_models_ready", return_value=True),
                patch("relay_tunnel.TUNNEL_RECONNECT_INITIAL_DELAY", 0.01),
            ):
                controller.start()
                real_start = controller.start

                def observed_start(*args, **kwargs):
                    try:
                        return real_start(*args, **kwargs)
                    except Exception:
                        if kwargs.get("_reconnect_generation") is not None:
                            retry_failed.set()
                            release_failure.wait(timeout=2)
                        raise

                with patch.object(controller, "start", side_effect=observed_start):
                    first.done.set()
                    self.assertTrue(retry_failed.wait(timeout=2))
                    manual = real_start()
                    self.assertEqual(manual["state"], "running")
                    self.assertIs(controller._process, replacement)
                    release_failure.set()
                    time.sleep(0.05)

                snapshot = controller.snapshot()
                self.assertEqual(snapshot["state"], "running")
                self.assertEqual(snapshot["url"], self.PUBLIC_URL)
                self.assertIs(controller._process, replacement)
                self.assertEqual(popen.call_count, 3)
        finally:
            release_failure.set()
            controller.stop()


class TunnelTelemetryTest(unittest.TestCase):
    def test_finish_sink_and_persisted_recent_restore(self):
        finished = []
        telemetry = relay_tunnel._TunnelTelemetry(on_finish=finished.append)
        ticket = telemetry.begin("203.0.113.8", "POST", "/v1/responses", 10)
        telemetry.dispatch(ticket, "public-model")
        telemetry.finish(ticket, 200, 20)
        self.assertEqual(len(finished), 1)
        self.assertEqual(finished[0]["ip"], "203.0.113.8")
        self.assertGreater(finished[0]["timestamp"], 0)

        stored = {**finished[0], "id": 7, "client_ip": finished[0]["ip"]}
        restored = relay_tunnel._TunnelTelemetry()
        restored.restore_recent((stored,))
        event = restored.snapshot()["recent"][0]
        self.assertEqual((event["id"], event["ip"]), (-7, "203.0.113.8"))

    def test_lifecycle_rpm_bounds_and_idle_eviction(self):
        now = [100.25]
        wall = [1_800_000_000.0]
        telemetry = relay_tunnel._TunnelTelemetry(
            max_clients=2,
            max_live=2,
            max_recent=2,
            idle_seconds=10,
            clock=lambda: now[0],
            wall_clock=lambda: wall[0],
        )

        first = telemetry.begin("203.0.113.1", "POST", "/v1/responses", 12)
        self.assertIsNotNone(first)
        queued = telemetry.snapshot()
        self.assertEqual((queued["actual_rpm"], queued["active"]), (1, 0))
        self.assertEqual(queued["live"][0]["state"], "queued")
        self.assertEqual(queued["clients"][0]["connected"], 1)
        telemetry.dispatch(first, "allowed-model")
        now[0] += 0.25
        active = telemetry.snapshot()
        self.assertEqual((active["active"], active["live"][0]["state"]), (1, "active"))
        telemetry.finish(first, 200, 34)

        for suffix, status in ((2, 403), (3, 502)):
            request_id = telemetry.begin(
                "203.0.113.1", "POST", "/v1/messages", suffix
            )
            telemetry.finish(request_id, status, suffix * 10)
        bounded = telemetry.snapshot()
        self.assertEqual(len(bounded["recent"]), 2)
        self.assertEqual(
            [event["status"] for event in bounded["recent"]], [403, 502]
        )
        self.assertEqual(bounded["clients"][0]["actual_rpm"], 3)

        second = telemetry.begin("203.0.113.2", "POST", "/v1/responses")
        self.assertIsNotNone(second)
        telemetry.finish(second, 200, 1)
        self.assertIsNone(
            telemetry.begin("203.0.113.3", "POST", "/v1/responses")
        )
        self.assertEqual(telemetry.snapshot()["clients_seen"], 2)

        now[0] += 11
        evicted = telemetry.snapshot()
        self.assertEqual((evicted["clients"], evicted["live"]), ((), ()))
        self.assertEqual((evicted["clients_seen"], evicted["actual_rpm"]), (0, 0))
        self.assertEqual(len(evicted["recent"]), 2)

    def test_concurrent_updates_keep_counts_and_memory_bounded(self):
        workers = 64
        telemetry = relay_tunnel._TunnelTelemetry(
            max_clients=8,
            max_live=32,
            max_recent=32,
            idle_seconds=60,
        )
        ready = threading.Barrier(workers + 1)
        release = threading.Event()
        errors = []

        def run(position):
            try:
                request_id = telemetry.begin(
                    f"198.51.100.{position % 8 + 1}",
                    "POST",
                    "/v1/responses",
                    position,
                )
                telemetry.dispatch(request_id, "allowed-model")
                ready.wait(timeout=5)
                release.wait(timeout=5)
                telemetry.finish(request_id, 200, position * 2)
            except BaseException as error:
                errors.append(error)

        threads = [threading.Thread(target=run, args=(index,)) for index in range(workers)]
        for thread in threads:
            thread.start()
        ready.wait(timeout=5)
        live = telemetry.snapshot()
        self.assertEqual((len(live["live"]), live["active"]), (32, workers))
        self.assertEqual(live["clients_seen"], 8)
        self.assertEqual(live["clients_connected"], 8)
        self.assertEqual(live["actual_rpm"], workers)
        release.set()
        for thread in threads:
            thread.join(timeout=5)
        self.assertFalse(any(thread.is_alive() for thread in threads))
        self.assertEqual(errors, [])
        completed = telemetry.snapshot()
        self.assertEqual((completed["live"], completed["active"]), ((), 0))
        self.assertEqual(completed["clients_connected"], 0)
        self.assertEqual(len(completed["recent"]), 32)


class RelayTest(unittest.TestCase):
    def test_local_and_tunnel_models_route_to_different_providers(self):
        class RoutedUpstreamHandler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                payload = json.loads(body)
                self.server.requests.append(
                    {
                        "model": payload.get("model"),
                        "authorization": self.headers.get("Authorization"),
                        "private_route": self.headers.get(
                            relay_tunnel.TUNNEL_MODEL_HEADER
                        ),
                    }
                )
                response = json.dumps(
                    {
                        "type": "response.completed",
                        "response": {
                            "status": "completed",
                            "error": None,
                            "incomplete_details": None,
                            "output": [],
                            "model": payload.get("model"),
                        },
                    },
                    separators=(",", ":"),
                ).encode()
                response = b"data: " + response + b"\n\n"
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Content-Length", str(len(response)))
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(response)

            def log_message(self, _format, *_args):
                pass

        gpt = start_server(RoutedUpstreamHandler, "GPT")
        claude = start_server(RoutedUpstreamHandler, "Claude")
        gpt.requests = []
        claude.requests = []
        gpt_spec = relay.ProviderRegistry.make_spec(
            "GPT Provider",
            f"http://127.0.0.1:{gpt.server_port}/v1",
            auth_mode="bearer",
            provider_id="gpt-provider",
        )
        claude_spec = relay.ProviderRegistry.make_spec(
            "Claude Provider",
            f"http://127.0.0.1:{claude.server_port}/v1",
            auth_mode="bearer",
            provider_id="claude-provider",
        )
        registry = relay.ProviderRegistry(
            (
                (gpt_spec, (("gpt-key", 0, ""),)),
                (claude_spec, (("claude-key", 0, ""),)),
            )
        )
        proxy = start_server(
            relay.RelayHandler,
            server_class=relay.RelayServer,
            registry=registry,
            history=False,
        )
        token = "public-route-token-" + "x" * 32
        gateway = None

        def post(address, model, headers=None):
            body = json.dumps(
                {"model": model, "input": "route", "stream": True},
                separators=(",", ":"),
            ).encode()
            connection = http.client.HTTPConnection(*address, timeout=3)
            connection.request(
                "POST",
                "/v1/responses",
                body,
                {"Content-Type": "application/json", **(headers or {})},
            )
            response = connection.getresponse()
            result = response.status, response.read()
            connection.close()
            return result

        try:
            proxy.set_tunnel_provider("gpt-provider")
            first_local_generation = proxy._local_cancel_event
            proxy.set_relay_allowed_models(("gpt-5.6-sol",))
            self.assertTrue(first_local_generation.is_set())
            second_local_generation = proxy._local_cancel_event
            proxy.set_tunnel_allowed_models(("gpt-5.6-sol",))
            self.assertIs(proxy._local_cancel_event, second_local_generation)
            proxy.set_tunnel_provider("claude-provider")
            proxy.set_relay_allowed_models(("claude-opus-5[1m]",))
            self.assertTrue(second_local_generation.is_set())
            proxy.set_tunnel_allowed_models(("claude-opus-5[1m]",))

            self.assertEqual(post(proxy.server_address, "gpt-5.6-sol")[0], 200)
            self.assertEqual(post(proxy.server_address, "claude-opus-5[1m]")[0], 200)
            spoof = relay_tunnel.model_route_token("claude-opus-5[1m]")
            self.assertEqual(
                post(
                    proxy.server_address,
                    "unassigned-model",
                    {relay_tunnel.TUNNEL_MODEL_HEADER: spoof},
                )[0],
                200,
            )

            gateway = TunnelGateway(
                proxy.server_address,
                token,
                proxy.tunnel_allowed_models(),
                sensitive_markers=proxy._tunnel_sensitive_markers(),
                secret_markers=proxy._tunnel_secret_markers(),
                route_guard=proxy._tunnel_route_safe,
                route_marker=proxy._tunnel_route_marker,
            )
            threading.Thread(target=gateway.serve_forever, daemon=True).start()
            public_headers = {
                "Authorization": f"Bearer {token}",
                relay_tunnel.CLIENT_IP_HEADER: "203.0.113.120",
            }
            self.assertEqual(
                post(gateway.server_address, "gpt-5.6-sol", public_headers)[0], 200
            )
            self.assertEqual(
                post(
                    gateway.server_address,
                    "claude-opus-5[1m]",
                    public_headers,
                )[0],
                200,
            )

            self.assertEqual(
                [request["model"] for request in gpt.requests],
                ["gpt-5.6-sol", "unassigned-model", "gpt-5.6-sol"],
            )
            self.assertEqual(
                [request["model"] for request in claude.requests],
                ["claude-opus-5[1m]", "claude-opus-5[1m]"],
            )
            self.assertTrue(
                all(request["private_route"] is None for request in gpt.requests + claude.requests)
            )
            self.assertEqual(gpt.requests[0]["authorization"], "Bearer gpt-key")
            self.assertEqual(
                claude.requests[0]["authorization"], "Bearer claude-key"
            )
            self.assertEqual(
                {route["model"]: route["provider_id"] for route in proxy.relay_model_routes()},
                {
                    "gpt-5.6-sol": "gpt-provider",
                    "claude-opus-5[1m]": "claude-provider",
                },
            )
            deadline = time.monotonic() + 1
            while proxy.metrics.snapshot()["active"] and time.monotonic() < deadline:
                time.sleep(0.01)
            claude_events = [
                event
                for event in proxy.metrics.snapshot()["recent"]
                if event["model"] == "claude-opus-5[1m]"
            ]
            self.assertTrue(claude_events)
            self.assertTrue(
                all(event["provider_id"] == "claude-provider" for event in claude_events)
            )
        finally:
            if gateway is not None:
                gateway.shutdown()
                gateway.server_close()
            for server in (proxy, gpt, claude):
                server.shutdown()
                server.server_close()

    def test_tunnel_provider_pin_is_independent_and_internal_route_is_unforgeable(self):
        local = start_server(UpstreamHandler, "Local")
        echo = start_server(UpstreamHandler, "EchoGate")
        local.auth_sequence = []
        echo.auth_sequence = []
        proxy = start_proxy(
            f"http://127.0.0.1:{local.server_port}",
            f"http://127.0.0.1:{echo.server_port}",
        )

        def post(headers=None):
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=2
            )
            body = b'{"model":"allowed-model"}'
            connection.request(
                "POST",
                "/v1/responses",
                body,
                {"Content-Type": "application/json", **(headers or {})},
            )
            response = connection.getresponse()
            result = response.status, response.getheaders(), response.read()
            connection.close()
            return result

        try:
            original_marker = proxy._tunnel_route_marker
            self.assertEqual(proxy.set_tunnel_provider("echo"), "echo")
            marker = proxy._tunnel_route_marker
            self.assertEqual(marker, original_marker)
            self.assertEqual(proxy.tunnel_provider_id(), "echo")
            self.assertEqual(proxy.tunnel_allowed_models(), ())
            proxy.set_tunnel_allowed_models(("allowed-model",))
            self.assertTrue(proxy._tunnel_route_safe())

            with patch.object(
                proxy, "fetch_models", return_value={"data": [{"id": "allowed-model"}]}
            ) as fetch:
                self.assertEqual(
                    proxy.fetch_tunnel_models(),
                    {"data": [{"id": "allowed-model"}]},
                )
                fetch.assert_called_once_with("echo")

            with patch.object(proxy.tunnel, "stop", wraps=proxy.tunnel.stop) as stop:
                proxy.select("echo")
                proxy.toggle()
                stop.assert_not_called()
            self.assertEqual(proxy.registry.active().id, "local")
            self.assertEqual(proxy.tunnel_provider_id(), "echo")
            self.assertEqual(proxy.tunnel_allowed_models(), ("allowed-model",))

            status, _headers, body = post()
            self.assertEqual(status, 201)
            self.assertEqual(json.loads(body)["provider"], "Local")

            status, _headers, body = post(
                {relay_http.TUNNEL_REQUEST_HEADER: "1"}
            )
            self.assertEqual((status, json.loads(body)), (403, {"error": "Request rejected"}))

            status, _headers, body = post(
                {relay_http.TUNNEL_REQUEST_HEADER: marker}
            )
            self.assertEqual((status, json.loads(body)), (403, {"error": "Request rejected"}))

            status, _headers, body = post(
                {
                    relay_http.TUNNEL_REQUEST_HEADER: marker,
                    relay_tunnel.TUNNEL_MODEL_HEADER: relay_tunnel.model_route_token(
                        "allowed-model"
                    ),
                }
            )
            self.assertEqual(status, 201)
            self.assertEqual(json.loads(body)["provider"], "EchoGate")

            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=2
            )
            body = b'{"model":"allowed-model"}'
            connection.putrequest("POST", "/v1/responses")
            connection.putheader("Content-Type", "application/json")
            connection.putheader("Content-Length", str(len(body)))
            connection.putheader(relay_http.TUNNEL_REQUEST_HEADER, marker)
            connection.putheader(relay_http.TUNNEL_REQUEST_HEADER, marker)
            connection.putheader(
                relay_tunnel.TUNNEL_MODEL_HEADER,
                relay_tunnel.model_route_token("allowed-model"),
            )
            connection.endheaders(body)
            response = connection.getresponse()
            duplicate_body = response.read()
            connection.close()
            self.assertEqual(
                (response.status, json.loads(duplicate_body)),
                (403, {"error": "Request rejected"}),
            )

            local.auth_sequence.clear()
            echo.auth_sequence.clear()
            public_token = "public-test-token-00000000000000000000000000000000"
            gateway = TunnelGateway(
                proxy.server_address,
                public_token,
                ("allowed-model",),
                sensitive_markers=proxy._tunnel_sensitive_markers(),
                secret_markers=proxy._tunnel_secret_markers(),
                route_guard=proxy._tunnel_route_safe,
                route_marker=marker,
            )
            gateway_thread = threading.Thread(
                target=gateway.serve_forever, daemon=True
            )
            gateway_thread.start()
            try:
                connection = http.client.HTTPConnection(
                    *gateway.server_address, timeout=2
                )
                connection.request(
                    "POST",
                    "/v1/responses",
                    b'{"model":"allowed-model"}',
                    {
                        "Authorization": f"Bearer {public_token}",
                        relay_tunnel.CLIENT_IP_HEADER: "203.0.113.30",
                        "Content-Type": "application/json",
                    },
                )
                response = connection.getresponse()
                public_headers = response.getheaders()
                public_body = response.read()
                connection.close()
                public_payload = json.loads(public_body)
                self.assertEqual(response.status, 200)
                self.assertNotIn("provider", public_payload)
                self.assertNotIn("authorization", public_payload)
                self.assertNotIn("x_api_key", public_payload)
                self.assertEqual((len(local.auth_sequence), len(echo.auth_sequence)), (0, 1))
                public_wire = (
                    "\n".join(f"{name}: {value}" for name, value in public_headers)
                ).encode() + public_body
                for private in (
                    "EchoGate",
                    "echo",
                    marker,
                    public_token,
                    f"127.0.0.1:{echo.server_port}",
                ):
                    self.assertNotIn(private.encode(), public_wire)
            finally:
                gateway.shutdown()
                gateway.server_close()
                gateway_thread.join(timeout=1)

            with patch.object(
                proxy.tunnel, "snapshot", return_value={"state": "running"}
            ):
                self.assertEqual(proxy.set_tunnel_provider("local"), "local")
            self.assertEqual(proxy.tunnel_provider_id(), "local")
            self.assertEqual(proxy._tunnel_route_marker, marker)
            self.assertEqual(proxy.tunnel_allowed_models(), ("allowed-model",))
            status, _headers, body = post(
                {
                    relay_http.TUNNEL_REQUEST_HEADER: marker,
                    relay_tunnel.TUNNEL_MODEL_HEADER: relay_tunnel.model_route_token(
                        "allowed-model"
                    ),
                }
            )
            self.assertEqual(status, 201)
            self.assertEqual(json.loads(body)["provider"], "EchoGate")
        finally:
            for server in (proxy, local, echo):
                server.shutdown()
                server.server_close()

    def test_fetch_tunnel_models_rejects_catalog_from_stale_pin_generation(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        started = threading.Event()
        release = threading.Event()
        results = []
        errors = []

        def fetch(provider_id):
            self.assertEqual(provider_id, "echo")
            started.set()
            release.wait(timeout=2)
            return {"data": [{"id": "stale-model"}]}

        try:
            proxy.set_tunnel_provider("echo")
            proxy.set_tunnel_allowed_models(("stale-model",))
            old_marker = proxy._tunnel_route_marker

            def load():
                try:
                    results.append(proxy.fetch_tunnel_models())
                except Exception as error:
                    errors.append(error)

            with patch.object(proxy, "fetch_models", side_effect=fetch):
                worker = threading.Thread(target=load)
                worker.start()
                self.assertTrue(started.wait(timeout=1))
                proxy.update_provider("echo", auth_mode="bearer")
                self.assertEqual(proxy.tunnel_provider_id(), "echo")
                self.assertNotEqual(proxy._tunnel_route_marker, old_marker)
                self.assertEqual(proxy.tunnel_allowed_models(), ())
                release.set()
                worker.join(timeout=2)
                self.assertFalse(worker.is_alive())

            self.assertEqual(results, [])
            self.assertEqual(len(errors), 1)
            self.assertIsInstance(errors[0], RuntimeError)
            self.assertEqual(str(errors[0]), "Tunnel model catalog is stale")
        finally:
            release.set()
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_tunnel_model_probe_uses_one_short_real_generation_without_leaks(self):
        upstream = start_server(UpstreamHandler, "ProbeProvider")
        upstream.probe_requests = []
        probe_key = "probe-secret-key"
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}",
            echo_api_key=probe_key,
        )
        try:
            proxy.set_tunnel_provider("echo")
            catalog = proxy.fetch_tunnel_models()
            self.assertEqual(
                {item["id"] for item in catalog["data"]},
                {"gpt-test", "claude-sonnet-test", "deepseek-test"},
            )
            self.assertEqual(proxy.tunnel_model_probes(), ())

            available = proxy.probe_tunnel_model("gpt-test")
            self.assertEqual(
                available, {"model": "gpt-test", "state": "available"}
            )
            self.assertEqual(len(upstream.probe_requests), 1)
            request = upstream.probe_requests[0]
            self.assertEqual(request["authorization"], f"Bearer {probe_key}")
            self.assertEqual(
                request["body"],
                {
                    "model": "gpt-test",
                    "messages": [{"role": "user", "content": "Reply OK"}],
                    "max_tokens": 1,
                    "stream": False,
                },
            )

            unavailable = None
            for rejected_status in (201, 202, 204, 200, 429):
                upstream.probe_status = rejected_status
                unavailable = proxy.probe_tunnel_model("claude-sonnet-test")
                self.assertEqual(
                    unavailable,
                    {"model": "claude-sonnet-test", "state": "unavailable"},
                )
            self.assertEqual(len(upstream.probe_requests), 6)
            with self.assertRaisesRegex(ValueError, "current catalog"):
                proxy.probe_tunnel_model("not-in-catalog")
            self.assertEqual(len(upstream.probe_requests), 6)

            snapshot = proxy.tunnel_model_probes()
            self.assertEqual(
                snapshot,
                (
                    {"model": "gpt-test", "state": "available"},
                    {"model": "claude-sonnet-test", "state": "unavailable"},
                ),
            )
            serialized = json.dumps((available, unavailable, snapshot))
            for private in (
                probe_key,
                "ProbeProvider",
                f"127.0.0.1:{upstream.server_port}",
                "Reply OK",
                "probe rejected",
            ):
                self.assertNotIn(private, serialized)
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_tunnel_model_probe_hard_timeout_exposes_testing_and_cleans_connection(self):
        upstream = start_server(UpstreamHandler, "ProbeProvider")
        upstream.probe_requests = []
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}",
            echo_api_key="probe-key",
        )
        upstream.probe_started = threading.Event()
        upstream.probe_release = threading.Event()
        result = []
        try:
            proxy.set_tunnel_provider("echo")
            proxy.fetch_tunnel_models()

            with patch("relay_http.TUNNEL_MODEL_PROBE_TIMEOUT", 0.15):
                started = time.monotonic()
                worker = threading.Thread(
                    target=lambda: result.append(
                        proxy.probe_tunnel_model("deepseek-test")
                    )
                )
                worker.start()
                self.assertTrue(upstream.probe_started.wait(timeout=1))
                self.assertEqual(
                    proxy.tunnel_model_probes(),
                    ({"model": "deepseek-test", "state": "testing"},),
                )
                worker.join(timeout=1)
                elapsed = time.monotonic() - started

            self.assertFalse(worker.is_alive())
            self.assertLess(elapsed, 0.75)
            self.assertEqual(
                result,
                [{"model": "deepseek-test", "state": "timeout"}],
            )
            self.assertEqual(len(upstream.probe_requests), 1)
            with proxy._upstream_lock:
                self.assertEqual(proxy._upstreams, {})

            deadline = time.monotonic() + 1
            while (
                proxy._tunnel_model_probe_slots._value
                < relay_http.MAX_TUNNEL_MODEL_PROBES
                and time.monotonic() < deadline
            ):
                time.sleep(0.01)
            self.assertEqual(
                proxy._tunnel_model_probe_slots._value,
                relay_http.MAX_TUNNEL_MODEL_PROBES,
            )
        finally:
            upstream.probe_release.set()
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_tunnel_model_probe_falls_through_plan_blocked_lite_key(self):
        upstream = start_server(UpstreamHandler, "ProbeProvider")
        upstream.probe_requests = []
        upstream.probe_block_lite_model = True
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}", echo_api_key="lite-key"
        )
        try:
            proxy.add_provider_key("echo", "pro-key")
            proxy.set_tunnel_provider("echo")
            proxy.fetch_tunnel_models()
            self.assertEqual(
                proxy.probe_tunnel_model("gpt-test"),
                {"model": "gpt-test", "state": "available"},
            )
            self.assertEqual(
                [item["authorization"] for item in upstream.probe_requests],
                ["Bearer lite-key", "Bearer pro-key"],
            )
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_tunnel_model_probe_deadline_includes_key_and_rpm_wait(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        upstream.probe_requests = []
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")

        def blocked_attempt(cancelled, _model):
            while not cancelled():
                time.sleep(0.01)
            raise relay_runtime.ClientDisconnected

        try:
            proxy.set_tunnel_provider("echo")
            proxy.fetch_tunnel_models()
            with (
                patch.object(
                    relay_runtime.ProviderRuntime,
                    "acquire_attempt",
                    side_effect=blocked_attempt,
                ),
                patch("relay_http.TUNNEL_MODEL_PROBE_TIMEOUT", 0.12),
            ):
                started = time.monotonic()
                result = proxy.probe_tunnel_model("gpt-test")
                elapsed = time.monotonic() - started
            self.assertEqual(result, {"model": "gpt-test", "state": "timeout"})
            self.assertLess(elapsed, 0.6)
            self.assertEqual(upstream.probe_requests, [])
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_tunnel_model_probe_timeout_releases_slot_before_next_model(self):
        upstream = start_server(UpstreamHandler, "ProbeProvider")
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        proxy._tunnel_model_probe_slots = threading.BoundedSemaphore(1)

        def probe(_provider_id, model, _deadline, cancelled, *_args):
            if model == "gpt-test":
                while not cancelled.is_set():
                    time.sleep(0.005)
                time.sleep(0.05)
                return "timeout"
            return "available"

        try:
            proxy.set_tunnel_provider("echo")
            proxy.fetch_tunnel_models()
            with patch("relay_http.TUNNEL_MODEL_PROBE_TIMEOUT", 0.03), patch.object(
                proxy, "_probe_tunnel_model_once", side_effect=probe
            ):
                self.assertEqual(
                    proxy.probe_tunnel_model("gpt-test")["state"], "timeout"
                )
                self.assertEqual(
                    proxy.probe_tunnel_model("claude-sonnet-test")["state"],
                    "available",
                )
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_pinned_key_mutations_invalidate_model_probe_states(self):
        upstream = start_server(UpstreamHandler, "ProbeProvider")
        upstream.probe_requests = []
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}",
            echo_api_key="primary-probe-key",
        )
        try:
            proxy.set_tunnel_provider("echo")
            proxy.fetch_tunnel_models()
            proxy.set_tunnel_allowed_models(("gpt-test",))
            marker = proxy._tunnel_route_marker

            with patch.object(proxy.tunnel, "stop") as stop:
                self.assertEqual(
                    proxy.probe_tunnel_model("gpt-test")["state"], "available"
                )
                fingerprint = proxy.add_provider_key("echo", "secondary-probe-key")
                self.assertEqual(proxy.tunnel_model_probes(), ())

                self.assertEqual(
                    proxy.probe_tunnel_model("gpt-test")["state"], "available"
                )
                proxy.update_provider_key("echo", fingerprint, 60)
                self.assertEqual(proxy.tunnel_model_probes(), ())

                self.assertEqual(
                    proxy.probe_tunnel_model("gpt-test")["state"], "available"
                )
                proxy.remove_provider_key("echo", fingerprint)
                self.assertEqual(proxy.tunnel_model_probes(), ())

            stop.assert_not_called()
            self.assertEqual(proxy._tunnel_route_marker, marker)
            self.assertEqual(proxy.tunnel_allowed_models(), ("gpt-test",))
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_tunnel_model_probe_discards_result_after_pin_change(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        entered = threading.Event()
        release = threading.Event()
        result = []

        def delayed_probe(*_args):
            entered.set()
            release.wait(timeout=2)
            return "available"

        try:
            proxy.set_tunnel_provider("echo")
            with patch.object(
                proxy,
                "fetch_models",
                return_value={"data": [{"id": "stale-model"}]},
            ):
                proxy.fetch_tunnel_models()
            with patch.object(
                proxy, "_probe_tunnel_model_once", side_effect=delayed_probe
            ):
                worker = threading.Thread(
                    target=lambda: result.append(
                        proxy.probe_tunnel_model("stale-model")
                    )
                )
                worker.start()
                self.assertTrue(entered.wait(timeout=1))
                self.assertEqual(
                    proxy.tunnel_model_probes(),
                    ({"model": "stale-model", "state": "testing"},),
                )
                old_marker = proxy._tunnel_route_marker
                proxy.update_provider("echo", auth_mode="bearer")
                self.assertEqual(proxy.tunnel_provider_id(), "echo")
                self.assertEqual(proxy._tunnel_route_marker, old_marker)
                self.assertEqual(proxy.tunnel_model_probes(), ())
                release.set()
                worker.join(timeout=1)
            self.assertFalse(worker.is_alive())
            self.assertEqual(
                result,
                [{"model": "stale-model", "state": "unavailable"}],
            )
            self.assertEqual(proxy.tunnel_model_probes(), ())
        finally:
            release.set()
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_pinned_provider_becoming_passthrough_revokes_tunnel_policy(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        lease = None
        try:
            proxy.set_tunnel_provider("echo")
            proxy.set_tunnel_allowed_models(("allowed-model",))
            old_marker = proxy._tunnel_route_marker
            with patch.object(proxy.tunnel, "stop") as metadata_stop:
                proxy.update_provider(
                    "echo",
                    name="EchoGate tuned",
                    rpm=31,
                    cache_1h=False,
                )
            metadata_stop.assert_not_called()
            self.assertEqual(proxy._tunnel_route_marker, old_marker)
            self.assertEqual(proxy.tunnel_allowed_models(), ("allowed-model",))
            lease = proxy.registry.acquire("echo")
            self.assertEqual(lease.spec.auth_mode, "auto")
            stop_saw_unlocked_config = []
            stop_saw_empty_policy = []

            def stop_outside_config_lock():
                stop_saw_empty_policy.append(proxy.tunnel_allowed_models() == ())
                acquired = threading.Event()

                def acquire_config_lock():
                    with proxy._config_lock:
                        acquired.set()

                checker = threading.Thread(target=acquire_config_lock)
                checker.start()
                checker.join(timeout=1)
                stop_saw_unlocked_config.append(acquired.is_set())

            with patch.object(
                proxy.tunnel, "stop", side_effect=stop_outside_config_lock
            ) as stop:
                updated = proxy.update_provider("echo", auth_mode="passthrough")

            self.assertEqual(updated.auth_mode, "passthrough")
            self.assertEqual(proxy.tunnel_allowed_models(), ())
            self.assertNotEqual(proxy._tunnel_route_marker, old_marker)
            self.assertFalse(proxy._tunnel_route_safe())
            self.assertEqual(stop_saw_unlocked_config, [True])
            self.assertEqual(stop_saw_empty_policy, [True])
            stop.assert_called_once_with()
            self.assertEqual(lease.spec.auth_mode, "auto")
            route_token = relay_tunnel.model_route_token("allowed-model")
            self.assertIsNone(
                proxy._acquire_request_route([old_marker], [route_token])
            )
            self.assertIsNone(
                proxy._acquire_request_route(
                    [proxy._tunnel_route_marker], [route_token]
                )
            )
        finally:
            if lease is not None:
                lease.release()
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
                "surrogate": "\ud800",
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
                    "X-OpenAI-Actor-Authorization": "local-relay",
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
            self.assertIsNone(local_result["actor_authorization"])
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
            self.assertIsNone(echo_result["actor_authorization"])
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
            self.assertEqual(echo_result["body"]["surrogate"], "\ud800")
            connection.close()

            connection, response = post("/v1/messages")
            anthropic_result = json.loads(response.read())
            self.assertIsNone(anthropic_result["authorization"])
            self.assertEqual(anthropic_result["x_api_key"], "test")
            self.assertIsNone(anthropic_result["actor_authorization"])
            self.assertNotIn("cache_control", anthropic_result["body"])
            self.assertEqual(
                anthropic_result["body"]["input"][0]["content"][0]
                ["cache_control"]["ttl"],
                "1h",
            )
            connection.close()

            connection, response = post("/v1/framing", b"{}")
            self.assertEqual(response.getheader("Content-Length"), "5")
            self.assertEqual(response.read(), b"hello")
            connection.close()

            with (
                patch("relay_http.STREAM_HEADER_TIMEOUT", 0.1),
                patch("relay_http.UPSTREAM_TIMEOUT", 0.1),
            ):
                connection, response = post(
                    "/v1/stream",
                    json.dumps({"model": "stream-model", "stream": True}).encode(),
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

            with patch("relay_http.retry_after_seconds", return_value=0.01):
                connection, response = post("/v1/truncated", b"{}")
            self.assertEqual(response.status, 200)
            self.assertEqual(response.getheader("Content-Length"), "20")
            self.assertEqual(response.read(), b"complete-after-retry")
            self.assertEqual(echo.truncated_count, 2)
            connection.close()

            snapshot = proxy.metrics.snapshot()
            self.assertEqual(snapshot["total"], 7)
            self.assertEqual(snapshot["active"], 0)
            self.assertEqual(snapshot["errors"], 0)
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
            echo_recovered = next(
                event for event in recovered if event["provider_id"] == "echo"
            )
            self.assertLess(echo_recovered["queue_ms"], 50)
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

    def test_multipart_image_routes_model_and_stream_without_rewriting_body(self):
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

        def multipart(boundary, *, stream=False):
            stream_part = (
                f"--{boundary}\r\n"
                'Content-Disposition: form-data; name="stream"\r\n\r\n'
                "true\r\n"
                if stream
                else ""
            )
            return (
                f"--{boundary}\r\n"
                'Content-Disposition: form-data; name="model"\r\n\r\n'
                "gpt-5.6-sol\r\n"
                + stream_part
                + f"--{boundary}\r\n"
                'Content-Disposition: form-data; name="image"; filename="image.png"\r\n'
                "Content-Type: image/png\r\n\r\n"
            ).encode() + b"synthetic-image" + f"\r\n--{boundary}--\r\n".encode()

        try:
            boundary = "local-image-fallback"
            body = multipart(boundary)
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=4
            )
            connection.request(
                "POST",
                "/model-fallback",
                body,
                {"Content-Type": f"multipart/form-data; boundary={boundary}"},
            )
            response = connection.getresponse()
            result = json.loads(response.read())
            connection.close()
            self.assertEqual(response.status, 201)
            self.assertEqual(result["model"], "gpt-5.6-sol")
            self.assertEqual(result["authorization"], "Bearer pro-key")
            self.assertEqual(
                upstream.auth_sequence, ["Bearer lite-key", "Bearer pro-key"]
            )

            stream_boundary = "local-image-stream"
            with (
                patch("relay_http.STREAM_HEADER_TIMEOUT", 0.1),
                patch("relay_http.UPSTREAM_TIMEOUT", 0.1),
            ):
                connection = http.client.HTTPConnection(
                    "127.0.0.1", proxy.server_port, timeout=3
                )
                connection.request(
                    "POST",
                    "/v1/stream",
                    multipart(stream_boundary, stream=True),
                    {
                        "Content-Type":
                        f"multipart/form-data; boundary={stream_boundary}"
                    },
                )
                response = connection.getresponse()
                started = time.monotonic()
                self.assertEqual(response.readline(), b"data: first\n")
                self.assertLess(time.monotonic() - started, 0.5)
                self.assertIn(b"data: second", response.read())
                connection.close()
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_local_chunked_multipart_is_bounded_and_byte_exact(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        upstream.auth_sequence = []
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}",
            f"http://127.0.0.1:{upstream.server_port}/v1",
            echo_api_key="lite-key",
        )
        proxy.select("echo")
        boundary = "local-chunked-image"
        content_type = f"multipart/form-data; boundary={boundary}"
        multipart = (
            f"--{boundary}\r\n"
            'Content-Disposition: form-data; name="model"\r\n\r\n'
            "another-model\r\n"
            f"--{boundary}\r\n"
            'Content-Disposition: form-data; name="image"; filename="image.png"\r\n'
            "Content-Type: image/png\r\n\r\n"
        ).encode() + b"synthetic-image" + f"\r\n--{boundary}--\r\n".encode()

        def raw(framing_headers, wire_body):
            client = socket.create_connection(("127.0.0.1", proxy.server_port), timeout=3)
            client.sendall(
                (
                    "POST /model-fallback HTTP/1.1\r\n"
                    f"Host: 127.0.0.1:{proxy.server_port}\r\n"
                    f"Content-Type: {content_type}\r\n"
                    + framing_headers
                    + "Connection: close\r\n\r\n"
                ).encode()
                + wire_body
            )
            response = http.client.HTTPResponse(client)
            response.begin()
            result = response.status, response.read()
            client.close()
            return result

        try:
            split = len(multipart) // 3
            wire = bytearray()
            for chunk in (
                multipart[:split],
                multipart[split : split * 2],
                multipart[split * 2 :],
            ):
                wire.extend(f"{len(chunk):x}\r\n".encode())
                wire.extend(chunk)
                wire.extend(b"\r\n")
            wire.extend(b"0\r\n\r\n")
            status, body = raw("Transfer-Encoding: chunked\r\n", bytes(wire))
            self.assertEqual(status, 201)
            self.assertEqual(json.loads(body)["model"], "another-model")
            self.assertEqual(upstream.last_body, multipart)
            self.assertEqual(upstream.last_content_type, content_type)

            original = upstream.last_body
            status, _body = raw(
                "Transfer-Encoding: chunked\r\n"
                f"Content-Length: {len(multipart)}\r\n",
                bytes(wire),
            )
            self.assertEqual(status, 400)
            self.assertEqual(upstream.last_body, original)
            status, _body = raw(
                "Transfer-Encoding: chunked\r\n",
                f"{relay_http.MAX_REQUEST_BYTES + 1:x}\r\n".encode(),
            )
            self.assertEqual(status, 413)
            self.assertEqual(upstream.last_body, original)
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

    def test_request_scoped_failures_do_not_cool_working_keys(self):
        paths = ("/transient-400", "/transient-500", "/transient-504", "/reset")
        for path in paths:
            with self.subTest(path=path):
                upstream = start_server(UpstreamHandler, "Upstream")
                upstream.auth_sequence = []
                proxy = start_proxy(
                    f"http://127.0.0.1:{upstream.server_port}",
                    f"http://127.0.0.1:{upstream.server_port}/v1",
                    echo_api_key="primary-key",
                )
                proxy.select("echo")
                proxy.add_provider_key("echo", "fallback-key")
                try:
                    connection = http.client.HTTPConnection(
                        "127.0.0.1", proxy.server_port, timeout=2
                    )
                    with patch("relay_http.retry_after_seconds", return_value=0.02):
                        connection.request(
                            "POST",
                            path,
                            b"{}",
                            {"Content-Type": "application/json"},
                        )
                        response = connection.getresponse()
                        response.read()
                    connection.close()

                    self.assertEqual(response.status, 201)
                    self.assertEqual(
                        upstream.auth_sequence,
                        ["Bearer primary-key", "Bearer primary-key"],
                    )
                    attempts = (
                        upstream.reset_count
                        if path == "/reset"
                        else upstream.transient_counts[f"/v1{path}"]
                    )
                    self.assertEqual(attempts, 2)
                    self.assertEqual(
                        [key["cooldown_ms"] for key in proxy.provider_keys("echo")],
                        [0.0, 0.0],
                    )
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

    def test_provider_switch_cancels_old_upstream_but_not_pinned_tunnel(self):
        class SwitchHandler(UpstreamHandler):
            def do_POST(self):
                if self.path == "/v1/switch-stream":
                    self.rfile.read(int(self.headers.get("Content-Length", "0")))
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.send_header("Connection", "close")
                    self.end_headers()
                    self.wfile.write(b"data: first\n\n")
                    self.wfile.flush()
                    self.server.stream_release.wait(5)
                    try:
                        self.wfile.write(b"data: second\n\n")
                    except OSError:
                        pass
                    return
                if self.path != "/v1/pinned-hang":
                    return super().do_POST()
                self.rfile.read(int(self.headers.get("Content-Length", "0")))
                self.server.pinned_started.set()
                self.server.pinned_release.wait(5)
                body = b'{"ok":true}'
                try:
                    self.send_response(201)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(body)))
                    self.send_header("Connection", "close")
                    self.end_headers()
                    self.wfile.write(body)
                except OSError:
                    pass

        old_upstream = start_server(SwitchHandler, "Old")
        old_upstream.hang_started = threading.Event()
        old_upstream.hang_release = threading.Event()
        old_upstream.stream_release = threading.Event()
        pinned_upstream = start_server(SwitchHandler, "Pinned")
        pinned_upstream.pinned_started = threading.Event()
        pinned_upstream.pinned_release = threading.Event()
        proxy = start_proxy(
            f"http://127.0.0.1:{old_upstream.server_port}",
            f"http://127.0.0.1:{pinned_upstream.server_port}/v1",
            echo_api_key="switch-test-key",
        )
        old_result = []
        stream_result = []
        stream_headers = threading.Event()
        pinned_result = []

        def post(path, result, headers=None, response_started=None):
            try:
                body = (
                    b'{"model":"pinned-model"}'
                    if headers and "X-Provider-Switch-Tunnel" in headers
                    else b"{}"
                )
                connection = http.client.HTTPConnection(
                    "127.0.0.1", proxy.server_port, timeout=3
                )
                connection.request(
                    "POST",
                    path,
                    body,
                    {"Content-Type": "application/json", **(headers or {})},
                )
                response = connection.getresponse()
                if response_started is not None:
                    first = response.readline()
                    response_started.set()
                else:
                    first = b""
                result.append((response.status, first + response.read()))
                connection.close()
            except Exception as error:
                result.append(error)

        try:
            proxy.set_tunnel_provider("echo")
            proxy.set_tunnel_allowed_models(("pinned-model",))
            same_event = proxy._local_cancel_event
            proxy.select("local")
            self.assertIs(proxy._local_cancel_event, same_event)
            self.assertFalse(same_event.is_set())

            old_thread = threading.Thread(
                target=post, args=("/v1/hang", old_result), daemon=True
            )
            pinned_thread = threading.Thread(
                target=post,
                args=(
                    "/pinned-hang",
                    pinned_result,
                    {
                        "X-Provider-Switch-Tunnel": proxy._tunnel_route_marker,
                        relay_tunnel.TUNNEL_MODEL_HEADER: relay_tunnel.model_route_token(
                            "pinned-model"
                        ),
                    },
                ),
                daemon=True,
            )
            stream_thread = threading.Thread(
                target=post,
                args=("/v1/switch-stream", stream_result, None, stream_headers),
                daemon=True,
            )
            old_thread.start()
            pinned_thread.start()
            stream_thread.start()
            self.assertTrue(old_upstream.hang_started.wait(1))
            self.assertTrue(pinned_upstream.pinned_started.wait(1))
            self.assertTrue(stream_headers.wait(1))

            switched_at = time.monotonic()
            proxy.select("echo")
            old_thread.join(1)
            stream_thread.join(1)
            self.assertFalse(old_thread.is_alive())
            self.assertFalse(stream_thread.is_alive())
            self.assertLess(time.monotonic() - switched_at, 1)
            self.assertEqual(old_result[0][0], 503)
            self.assertEqual(stream_result, [(200, b"data: first\n\n")])
            self.assertTrue(pinned_thread.is_alive())

            fresh = []
            post("/fresh", fresh)
            self.assertEqual(fresh[0][0], 201)
            self.assertIn(b'"provider": "Pinned"', fresh[0][1])

            pinned_upstream.pinned_release.set()
            pinned_thread.join(1)
            self.assertFalse(pinned_thread.is_alive())
            self.assertEqual(pinned_result, [(201, b'{"ok":true}')])

            proxy.select("local")
            old_upstream.hang_started.clear()
            edited_result = []
            edited_thread = threading.Thread(
                target=post,
                args=("/v1/hang", edited_result),
                daemon=True,
            )
            edited_thread.start()
            self.assertTrue(old_upstream.hang_started.wait(1))
            proxy.update_provider(
                "local",
                upstream=f"http://127.0.0.1:{pinned_upstream.server_port}",
            )
            edited_thread.join(1)
            self.assertFalse(edited_thread.is_alive())
            self.assertEqual(edited_result[0][0], 503)

            proxy.update_provider(
                "local",
                upstream=f"http://127.0.0.1:{old_upstream.server_port}",
            )
            old_upstream.hang_started.clear()
            deleted_result = []
            deleted_thread = threading.Thread(
                target=post,
                args=("/v1/hang", deleted_result),
                daemon=True,
            )
            deleted_thread.start()
            self.assertTrue(old_upstream.hang_started.wait(1))
            proxy.delete_provider("local")
            deleted_thread.join(1)
            self.assertFalse(deleted_thread.is_alive())
            self.assertEqual(deleted_result[0][0], 503)
            self.assertEqual(proxy.provider()[0], "EchoGate")
            snapshot = proxy.metrics.snapshot()
            self.assertEqual(snapshot["errors"], 0)
            self.assertEqual(snapshot["cancelled"], 4)
        finally:
            old_upstream.hang_release.set()
            old_upstream.stream_release.set()
            pinned_upstream.pinned_release.set()
            for server in (proxy, old_upstream, pinned_upstream):
                server.shutdown()
                server.server_close()

    def test_provider_switch_interrupts_incomplete_request_bodies(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}",
            f"http://127.0.0.1:{upstream.server_port}/v1",
        )
        cases = (
            (b"Content-Length: 10\r\n\r\n12345", "content-length"),
            (b"Transfer-Encoding: chunked\r\n\r\nA\r\n12345", "chunked"),
        )
        try:
            for wire, name in cases:
                with self.subTest(framing=name):
                    proxy.select("local")
                    total = proxy.metrics.snapshot()["total"]
                    client = socket.create_connection(
                        ("127.0.0.1", proxy.server_port), timeout=2
                    )
                    try:
                        client.sendall(
                            b"POST /v1/responses HTTP/1.1\r\n"
                            b"Host: 127.0.0.1\r\n"
                            b"Content-Type: application/json\r\n"
                            + wire
                        )
                        deadline = time.monotonic() + 1
                        while (
                            proxy.metrics.snapshot()["total"] == total
                            and time.monotonic() < deadline
                        ):
                            time.sleep(0.01)
                        self.assertEqual(proxy.metrics.snapshot()["total"], total + 1)

                        switched_at = time.monotonic()
                        proxy.select("echo")
                        response = http.client.HTTPResponse(client)
                        response.begin()
                        self.assertLess(time.monotonic() - switched_at, 1)
                        self.assertEqual(response.status, 503)
                        self.assertEqual(
                            json.loads(response.read()),
                            {"error": "Request cancelled"},
                        )
                    finally:
                        client.close()

            client = socket.create_connection(
                ("127.0.0.1", proxy.server_port), timeout=2
            )
            try:
                client.sendall(
                    b"POST /v1/responses HTTP/1.1\r\n"
                    b"Host: 127.0.0.1\r\n"
                    b"Content-Length: 10\r\n\r\n12345"
                )
                client.shutdown(socket.SHUT_WR)
                response = http.client.HTTPResponse(client)
                response.begin()
                self.assertEqual(response.status, 400)
            finally:
                client.close()
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_provider_switch_cannot_cross_response_header_commit(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}",
            f"http://127.0.0.1:{upstream.server_port}/v1",
        )
        entered = threading.Event()
        release = threading.Event()
        switched = threading.Event()
        result = []
        original = relay_http.RelayHandler.send_response

        def pause_before_headers(handler, status, message=None):
            if status == 201 and not entered.is_set():
                entered.set()
                release.wait(2)
            return original(handler, status, message)

        def post():
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=3
            )
            try:
                connection.request("POST", "/v1/commit", b"{}")
                response = connection.getresponse()
                result.append(response.status)
                try:
                    response.read()
                except http.client.IncompleteRead:
                    pass
            except Exception as error:
                result.append(error)
            finally:
                connection.close()

        try:
            with patch.object(
                relay_http.RelayHandler,
                "send_response",
                pause_before_headers,
            ):
                request_thread = threading.Thread(target=post, daemon=True)
                request_thread.start()
                self.assertTrue(entered.wait(1))
                switch_thread = threading.Thread(
                    target=lambda: (proxy.select("echo"), switched.set()),
                    daemon=True,
                )
                switch_thread.start()
                self.assertTrue(switched.wait(1))
                release.set()
                request_thread.join(1)
                switch_thread.join(1)
                self.assertFalse(request_thread.is_alive())
                self.assertFalse(switch_thread.is_alive())
                self.assertEqual(len(result), 1)
                self.assertIsInstance(result[0], Exception)
        finally:
            release.set()
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_provider_switch_interrupts_slow_response_client(self):
        class FloodHandler(UpstreamHandler):
            def do_POST(self):
                self.rfile.read(int(self.headers.get("Content-Length", "0")))
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Connection", "close")
                self.end_headers()
                chunk = b"data: " + (b"x" * (64 * 1024 - 8)) + b"\n\n"
                self.server.flood_started.set()
                try:
                    for _ in range(2048):
                        self.wfile.write(chunk)
                except OSError:
                    pass

        upstream = start_server(FloodHandler, "Flood")
        upstream.flood_started = threading.Event()
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}",
            f"http://127.0.0.1:{upstream.server_port}/v1",
        )
        connection = http.client.HTTPConnection(
            "127.0.0.1", proxy.server_port, timeout=3
        )
        pinned_connection = None
        stopped = False
        try:
            proxy.set_tunnel_provider("echo")
            proxy.set_tunnel_allowed_models(("flood",))
            connection.connect()
            connection.sock.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 4096)
            body = b'{"model":"flood","stream":true}'
            connection.request(
                "POST",
                "/v1/flood",
                body,
                {"Content-Type": "application/json"},
            )
            response = connection.getresponse()
            self.assertEqual(response.status, 200)
            self.assertTrue(upstream.flood_started.wait(1))

            switched_at = time.monotonic()
            proxy.select("echo")
            deadline = time.monotonic() + 1
            while proxy.metrics.snapshot()["active"] and time.monotonic() < deadline:
                time.sleep(0.01)
            self.assertLess(time.monotonic() - switched_at, 1)
            self.assertEqual(proxy.metrics.snapshot()["active"], 0)

            connection.close()
            upstream.flood_started.clear()
            pinned_connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=3
            )
            pinned_connection.connect()
            pinned_connection.sock.setsockopt(
                socket.SOL_SOCKET, socket.SO_RCVBUF, 4096
            )
            pinned_connection.request(
                "POST",
                "/v1/flood",
                body,
                {
                    "Content-Type": "application/json",
                    "X-Provider-Switch-Tunnel": proxy._tunnel_route_marker,
                    relay_tunnel.TUNNEL_MODEL_HEADER: relay_tunnel.model_route_token(
                        "flood"
                    ),
                },
            )
            self.assertEqual(pinned_connection.getresponse().status, 200)
            self.assertTrue(upstream.flood_started.wait(1))
            proxy.shutdown()
            stopped = True
            deadline = time.monotonic() + 1
            while proxy.metrics.snapshot()["active"] and time.monotonic() < deadline:
                time.sleep(0.01)
            self.assertEqual(proxy.metrics.snapshot()["active"], 0)
        finally:
            connection.close()
            if pinned_connection is not None:
                pinned_connection.close()
            if not stopped:
                proxy.shutdown()
            proxy.server_close()
            upstream.shutdown()
            upstream.server_close()

    def test_provider_switch_cancels_model_catalog_request(self):
        class HangingModelsHandler(UpstreamHandler):
            def do_GET(self):
                self.server.models_started.set()
                self.server.models_release.wait(5)
                try:
                    super().do_GET()
                except OSError:
                    pass

        upstream = start_server(HangingModelsHandler, "Upstream")
        upstream.models_started = threading.Event()
        upstream.models_release = threading.Event()
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}",
            f"http://127.0.0.1:{upstream.server_port}/v1",
        )
        result = []

        def fetch():
            try:
                result.append(proxy.fetch_models("local"))
            except Exception as error:
                result.append(error)

        worker = threading.Thread(target=fetch, daemon=True)
        try:
            worker.start()
            self.assertTrue(upstream.models_started.wait(1))
            switched_at = time.monotonic()
            proxy.select("echo")
            worker.join(1)
            self.assertFalse(worker.is_alive())
            self.assertLess(time.monotonic() - switched_at, 1)
            self.assertIsInstance(result[0], relay_runtime.ClientDisconnected)
        finally:
            upstream.models_release.set()
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_provider_switch_interrupts_dns_resolution(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        proxy = start_proxy(
            f"https://stall.invalid:{upstream.server_port}",
            f"http://127.0.0.1:{upstream.server_port}/v1",
        )
        dns_started = threading.Event()
        dns_release = threading.Event()
        dns_returned = threading.Event()
        original = socket.getaddrinfo
        result = []

        def stalled(host, port, *args, **kwargs):
            if host != "stall.invalid":
                return original(host, port, *args, **kwargs)
            dns_started.set()
            dns_release.wait(5)
            try:
                return original("127.0.0.1", port, *args, **kwargs)
            finally:
                dns_returned.set()

        def post():
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=3
            )
            try:
                connection.request("POST", "/v1/dns", b"{}")
                response = connection.getresponse()
                result.append((response.status, response.read()))
            finally:
                connection.close()

        worker = threading.Thread(target=post, daemon=True)
        try:
            with patch("relay_http.socket.getaddrinfo", side_effect=stalled):
                worker.start()
                self.assertTrue(dns_started.wait(1))
                switched_at = time.monotonic()
                proxy.select("echo")
                worker.join(1)
                self.assertFalse(worker.is_alive())
                self.assertLess(time.monotonic() - switched_at, 1)
                self.assertEqual(
                    result,
                    [(503, b'{"error": "Request cancelled"}')],
                )
                self.assertFalse(hasattr(upstream, "last_body"))
                dns_release.set()
                self.assertTrue(dns_returned.wait(1))
        finally:
            dns_release.set()
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_provider_switch_interrupts_tls_handshake(self):
        listener = socket.socket()
        listener.bind(("127.0.0.1", 0))
        listener.listen()
        accepted = threading.Event()
        release = threading.Event()

        def blackhole():
            connection = None
            try:
                connection, _address = listener.accept()
                accepted.set()
                release.wait(5)
            finally:
                if connection is not None:
                    connection.close()

        blackhole_thread = threading.Thread(target=blackhole, daemon=True)
        blackhole_thread.start()
        upstream = start_server(UpstreamHandler, "Upstream")
        proxy = start_proxy(
            f"https://127.0.0.1:{listener.getsockname()[1]}",
            f"http://127.0.0.1:{upstream.server_port}/v1",
        )
        result = []

        def post():
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=3
            )
            try:
                connection.request("POST", "/v1/tls", b"{}")
                response = connection.getresponse()
                result.append((response.status, response.read()))
            finally:
                connection.close()

        worker = threading.Thread(target=post, daemon=True)
        try:
            worker.start()
            self.assertTrue(accepted.wait(1))
            switched_at = time.monotonic()
            proxy.select("echo")
            worker.join(1)
            self.assertFalse(worker.is_alive())
            self.assertLess(time.monotonic() - switched_at, 1)
            self.assertEqual(
                result,
                [(503, b'{"error": "Request cancelled"}')],
            )
        finally:
            release.set()
            listener.close()
            blackhole_thread.join(1)
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_provider_switch_interrupts_retry_sleep(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        proxy = start_proxy(
            f"http://127.0.0.1:{upstream.server_port}",
            f"http://127.0.0.1:{upstream.server_port}/v1",
            echo_api_key="switch-test-key",
        )
        result = []

        def post():
            connection = http.client.HTTPConnection(
                "127.0.0.1", proxy.server_port, timeout=3
            )
            try:
                connection.request(
                    "POST",
                    "/v1/transient-503",
                    b"{}",
                    {"Content-Type": "application/json"},
                )
                response = connection.getresponse()
                result.append(response.status)
                response.read()
            finally:
                connection.close()

        worker = threading.Thread(target=post, daemon=True)
        try:
            worker.start()
            deadline = time.monotonic() + 1
            while (
                proxy.metrics.snapshot()["retries"] < 1
                and time.monotonic() < deadline
            ):
                time.sleep(0.01)
            self.assertEqual(proxy.metrics.snapshot()["retries"], 1)

            switched_at = time.monotonic()
            proxy.select("echo")
            worker.join(1)
            self.assertFalse(worker.is_alive())
            self.assertLess(time.monotonic() - switched_at, 1)
            self.assertEqual(result, [503])
            self.assertEqual(upstream.transient_counts["/v1/transient-503"], 1)
        finally:
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_provider_toggle_interrupts_rate_and_key_queues(self):
        for queue_kind in ("rate", "key"):
            with self.subTest(queue=queue_kind):
                upstream = start_server(UpstreamHandler, "Upstream")
                proxy = start_proxy(
                    f"http://127.0.0.1:{upstream.server_port}",
                    f"http://127.0.0.1:{upstream.server_port}/v1",
                    echo_api_key="switch-test-key",
                )

                def post(path):
                    connection = http.client.HTTPConnection(
                        "127.0.0.1", proxy.server_port, timeout=3
                    )
                    try:
                        connection.request(
                            "POST",
                            path,
                            b"{}",
                            {"Content-Type": "application/json"},
                        )
                        response = connection.getresponse()
                        status = response.status
                        response.read()
                        return status
                    finally:
                        connection.close()

                result = []
                try:
                    if queue_kind == "rate":
                        proxy.update_provider("local", rpm=1)
                    else:
                        proxy.select("echo")
                        key_id = proxy.provider_keys("echo")[0]["id"]
                        proxy.update_provider_key("echo", key_id, 1)
                    self.assertEqual(post("/v1/prime"), 201)

                    worker = threading.Thread(
                        target=lambda: result.append(post("/v1/queued")),
                        daemon=True,
                    )
                    worker.start()
                    deadline = time.monotonic() + 1
                    while (
                        not any(provider.queued for provider in proxy.providers())
                        and time.monotonic() < deadline
                    ):
                        time.sleep(0.01)
                    self.assertTrue(any(provider.queued for provider in proxy.providers()))

                    switched_at = time.monotonic()
                    proxy.toggle()
                    worker.join(1)
                    self.assertFalse(worker.is_alive())
                    self.assertLess(time.monotonic() - switched_at, 1)
                    self.assertEqual(result, [503])
                    self.assertEqual(post("/v1/fresh"), 201)
                finally:
                    for server in (proxy, upstream):
                        server.shutdown()
                        server.server_close()

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

    def test_client_disconnect_interrupts_header_wait_and_marks_request_cancelled(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        upstream.hang_started = threading.Event()
        upstream.hang_release = threading.Event()
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        client = socket.create_connection(("127.0.0.1", proxy.server_port))
        body = b'{"model":"gpt-test","stream":true}'
        try:
            client.sendall(
                b"POST /v1/hang HTTP/1.1\r\n"
                b"Host: 127.0.0.1\r\n"
                b"Content-Type: application/json\r\n"
                + f"Content-Length: {len(body)}\r\n\r\n".encode()
                + body
            )
            self.assertTrue(upstream.hang_started.wait(1))
            snapshot = proxy.snapshot()
            self.assertEqual(snapshot["queued"], 0)
            self.assertEqual(snapshot["live"][0]["state"], "active")
            self.assertLess(snapshot["live"][0]["queue_ms"], 100)

            disconnected_at = time.monotonic()
            client.close()
            deadline = disconnected_at + 1
            while proxy.metrics.snapshot()["active"] and time.monotonic() < deadline:
                time.sleep(0.01)

            snapshot = proxy.metrics.snapshot()
            self.assertEqual(snapshot["active"], 0)
            self.assertLess(time.monotonic() - disconnected_at, 1)
            self.assertEqual(snapshot["recent"][-1]["state"], "cancelled")
            self.assertEqual(snapshot["recent"][-1]["error_detail"], "Client disconnected")
        finally:
            client.close()
            upstream.hang_release.set()
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    @unittest.skipUnless(os.name == "nt", "Windows select socket limit")
    def test_disconnect_monitor_chunks_more_than_512_windows_sockets(self):
        class WatchedUpstream:
            def __init__(self):
                self.closed = threading.Event()

            def close(self):
                self.closed.set()

        monitor = object.__new__(relay.RelayServer)
        monitor.stopping = threading.Event()
        monitor._upstream_lock = threading.Lock()
        pairs = [socket.socketpair() for _ in range(513)]
        upstreams = [WatchedUpstream() for _ in pairs]
        monitor._upstreams = {
            upstream: (pair[0], None, None)
            for upstream, pair in zip(upstreams, pairs)
        }
        thread = threading.Thread(
            target=monitor._monitor_client_disconnects, daemon=True
        )
        try:
            pairs[0][0].close()  # Make the first select chunk fail.
            pairs[-1][1].close()  # EOF in the second chunk must still be handled.
            with patch(
                "relay_http.select.select", wraps=relay_http.select.select
            ) as selected:
                thread.start()
                self.assertTrue(upstreams[-1].closed.wait(1))
                time.sleep(0.25)
                monitor.stopping.set()
                thread.join(1)
                self.assertFalse(thread.is_alive())
                self.assertLess(selected.call_count, 30)
        finally:
            monitor.stopping.set()
            if thread.ident is not None:
                thread.join(1)
            for left, right in pairs:
                left.close()
                right.close()

    def test_responses_sse_eof_before_terminal_retries_before_client_commit(self):
        class TruncatedResponsesHandler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_POST(self):
                request = json.loads(
                    self.rfile.read(int(self.headers.get("Content-Length", "0")))
                )
                self.server.stream_values.append(request.get("stream"))
                self.server.accept_values.append(self.headers.get("Accept"))
                self.server.accept_encodings.append(self.headers.get("Accept-Encoding"))
                self.server.rewritten_headers.append(
                    {
                        name.casefold(): self.headers.get(name)
                        for name in (
                            "Content-Type",
                            "Content-Digest",
                            "Signature",
                            "Signature-Input",
                        )
                    }
                )
                self.server.attempts += 1
                if self.server.attempts == 1:
                    body = (
                        b'data: {"type":"response.output_text.delta",'
                        b'"delta":"discard-first-attempt"}\n\n'
                    )
                    content_type = "text/event-stream"
                else:
                    completed = self.server.attempts >= 3
                    body = json.dumps(
                        {
                            "status": "completed" if completed else "incomplete",
                            "error": None,
                            "incomplete_details": (
                                None if completed else {"reason": "max_output_tokens"}
                            ),
                            "output": [
                                {
                                    "type": "message",
                                    "status": "completed",
                                    "content": [
                                        {
                                            "type": "output_text",
                                            "text": (
                                                "keep-completed-fallback"
                                                if completed
                                                else "discard-incomplete-fallback"
                                            ),
                                        }
                                    ],
                                }
                            ],
                        },
                        separators=(",", ":"),
                    ).encode()
                    content_type = "application/json"
                self.send_response(200)
                self.send_header("Content-Type", content_type)
                self.send_header("Content-Encoding", "identity")
                self.send_header("Content-Digest", "sha-256=:stale:")
                self.send_header("ETag", '"upstream-body"')
                self.send_header("Repr-Digest", "sha-256=:stale:")
                self.send_header("Signature", "stale=:signature:")
                self.send_header("Signature-Input", 'stale=("content-digest")')
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, _format, *args):
                pass

        upstream = start_server(TruncatedResponsesHandler, "Upstream")
        upstream.attempts = 0
        upstream.accept_values = []
        upstream.accept_encodings = []
        upstream.rewritten_headers = []
        upstream.stream_values = []
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        connection = http.client.HTTPConnection(
            "127.0.0.1", proxy.server_port, timeout=3
        )
        try:
            with patch("relay_http.retry_after_seconds", return_value=0.01):
                connection.request(
                    "POST",
                    "/v1/responses/",
                    b'{"model":"gpt-test","stream":true,"input":"\\ud800"}',
                    {
                        "Content-Type": "application/json; charset=utf-8",
                        "Content-Digest": "sha-256=:original:",
                        "Signature": "original=:signature:",
                        "Signature-Input": 'original=("content-digest")',
                    },
                )
                response = connection.getresponse()
                body = response.read()

            self.assertEqual(response.status, 200)
            self.assertEqual(upstream.attempts, 3)
            self.assertEqual(upstream.stream_values, [True, False, False])
            self.assertEqual(
                upstream.accept_values,
                [None, "application/json", "application/json"],
            )
            self.assertEqual(
                upstream.accept_encodings, ["identity", "identity", "identity"]
            )
            self.assertEqual(
                upstream.rewritten_headers[0],
                {
                    "content-type": "application/json; charset=utf-8",
                    "content-digest": "sha-256=:original:",
                    "signature": "original=:signature:",
                    "signature-input": 'original=("content-digest")',
                },
            )
            for headers in upstream.rewritten_headers[1:]:
                self.assertEqual(
                    headers,
                    {
                        "content-type": "application/json; charset=utf-8",
                        "content-digest": None,
                        "signature": None,
                        "signature-input": None,
                    },
                )
            self.assertEqual(
                response.getheader("Content-Type"),
                "text/event-stream; charset=utf-8",
            )
            self.assertIsNone(response.getheader("Content-Encoding"))
            self.assertIsNone(response.getheader("Content-Digest"))
            self.assertIsNone(response.getheader("ETag"))
            self.assertIsNone(response.getheader("Repr-Digest"))
            self.assertIsNone(response.getheader("Signature"))
            self.assertIsNone(response.getheader("Signature-Input"))
            self.assertNotIn(b"discard-first-attempt", body)
            self.assertNotIn(b"discard-incomplete-fallback", body)
            self.assertIn(b"keep-completed-fallback", body)
            self.assertIn(b'"type":"response.completed"', body)
            completed = json.loads(body.removeprefix(b"data: ").strip())
            self.assertEqual(completed["response"]["status"], "completed")
            self.assertIsNone(completed["response"]["incomplete_details"])
        finally:
            connection.close()
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_responses_multipart_retry_preserves_the_original_body(self):
        class MultipartResponsesHandler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                self.server.requests.append((self.headers.get("Content-Type"), body))
                response_body = (
                    b'data: {"type":"response.output_text.delta","delta":"retry"}\n\n'
                    if len(self.server.requests) == 1
                    else b'data: {"type":"response.completed","response":'
                    b'{"status":"completed","error":null,"output":[]}}\n\n'
                )
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(response_body)

            def log_message(self, _format, *args):
                pass

        boundary = "json-boundary"
        request_body = (
            b"--json-boundary\r\n"
            b'Content-Disposition: form-data; name="model"\r\n\r\n'
            b"gpt-test\r\n"
            b"--json-boundary\r\n"
            b'Content-Disposition: form-data; name="stream"\r\n\r\n'
            b"true\r\n"
            b"--json-boundary\r\n"
            b'Content-Disposition: form-data; name="input"\r\n\r\n'
            b"preserve-this-payload\r\n"
            b"--json-boundary--\r\n"
        )
        upstream = start_server(MultipartResponsesHandler, "Upstream")
        upstream.requests = []
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        connection = http.client.HTTPConnection(
            "127.0.0.1", proxy.server_port, timeout=3
        )
        try:
            with patch("relay_http.retry_after_seconds", return_value=0.01):
                connection.request(
                    "POST",
                    "/v1/responses",
                    request_body,
                    {"Content-Type": f"multipart/form-data; boundary={boundary}"},
                )
                response = connection.getresponse()
                response_body = response.read()
            self.assertEqual(response.status, 200)
            self.assertEqual(
                upstream.requests,
                [
                    (f"multipart/form-data; boundary={boundary}", request_body),
                    (f"multipart/form-data; boundary={boundary}", request_body),
                ],
            )
            self.assertNotIn(b'"delta":"retry"', response_body)
            self.assertIn(b'"type":"response.completed"', response_body)
        finally:
            connection.close()
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_responses_json_and_no_body_successes_retry_before_commit(self):
        class InvalidSuccessHandler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_POST(self):
                request = json.loads(
                    self.rfile.read(int(self.headers.get("Content-Length", "0")))
                )
                self.server.stream_values.append(request.get("stream"))
                attempt = len(self.server.stream_values)
                if attempt == 1:
                    body = (
                        b'{"type":"response.completed","response":'
                        b'{"status":"completed","error":null,"output":[]},'
                        b'"marker":"raw-json-must-not-pass"}'
                    )
                elif attempt == 2:
                    self.send_response(204)
                    self.send_header("Connection", "close")
                    self.end_headers()
                    return
                else:
                    body = (
                        b'{"status":"completed","error":null,'
                        b'"output":[],"marker":"final-json"}'
                    )
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, _format, *args):
                pass

        upstream = start_server(InvalidSuccessHandler, "Upstream")
        upstream.stream_values = []
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        connection = http.client.HTTPConnection(
            "127.0.0.1", proxy.server_port, timeout=3
        )
        try:
            with patch("relay_http.retry_after_seconds", return_value=0.01):
                connection.request(
                    "POST",
                    "/v1/responses",
                    b'{"model":"gpt-test","stream":true}',
                    {"Content-Type": "application/json"},
                )
                response = connection.getresponse()
                body = response.read()
            self.assertEqual(response.status, 200)
            self.assertEqual(upstream.stream_values, [True, False, False])
            self.assertEqual(
                response.getheader("Content-Type"),
                "text/event-stream; charset=utf-8",
            )
            self.assertNotIn(b"raw-json-must-not-pass", body)
            self.assertIn(b"final-json", body)
            self.assertIn(b'"type":"response.completed"', body)
        finally:
            connection.close()
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_stream_header_timeout_retries_without_exposing_504(self):
        upstream = start_server(UpstreamHandler, "Upstream")
        upstream.header_retry_count = 0
        upstream.header_retry_started = threading.Event()
        upstream.header_retry_release = threading.Event()
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        connection = http.client.HTTPConnection(
            "127.0.0.1", proxy.server_port, timeout=3
        )
        body = b'{"model":"gpt-test","stream":true}'
        try:
            with (
                patch("relay_http.STREAM_HEADER_TIMEOUT", 0.1),
                patch("relay_http.retry_after_seconds", return_value=0.01),
            ):
                connection.request(
                    "POST",
                    "/v1/header-retry",
                    body,
                    {"Content-Type": "application/json"},
                )
                response = connection.getresponse()
                self.assertEqual(response.status, 201)
                response.read()
            self.assertTrue(upstream.header_retry_started.is_set())
            self.assertEqual(upstream.header_retry_count, 2)
            deadline = time.monotonic() + 1
            while proxy.metrics.snapshot()["active"] and time.monotonic() < deadline:
                time.sleep(0.01)
            event = proxy.metrics.snapshot()["recent"][-1]
            self.assertEqual((event["state"], event["status"]), ("ok", 201))
            self.assertEqual(event["retries"], 1)
        finally:
            connection.close()
            upstream.header_retry_release.set()
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_stream_body_idle_timeout_retries_without_committing_partial_sse(self):
        class IdleStreamHandler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_POST(self):
                request = json.loads(
                    self.rfile.read(int(self.headers.get("Content-Length", "0")))
                )
                self.server.stream_values.append(request.get("stream"))
                if len(self.server.stream_values) == 1:
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.send_header("Connection", "close")
                    self.end_headers()
                    self.wfile.write(
                        b'data: {"type":"response.output_text.delta",'
                        b'"delta":"discard"}\n\n'
                    )
                    self.wfile.flush()
                    self.server.idle_started.set()
                    self.server.idle_release.wait(5)
                    return
                body = b'{"status":"completed","error":null,"output":[]}'
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, _format, *_args):
                pass

        upstream = start_server(IdleStreamHandler, "Upstream")
        upstream.stream_values = []
        upstream.idle_started = threading.Event()
        upstream.idle_release = threading.Event()
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        connection = http.client.HTTPConnection(
            "127.0.0.1", proxy.server_port, timeout=3
        )
        try:
            with (
                patch("relay_http.STREAM_IDLE_TIMEOUT", 0.1),
                patch("relay_http.STREAM_HEARTBEAT_INTERVAL", 0.01),
                patch("relay_http.retry_after_seconds", return_value=0.01),
            ):
                started = time.monotonic()
                connection.request(
                    "POST",
                    "/v1/responses",
                    b'{"model":"gpt-test","stream":true}',
                    {"Content-Type": "application/json"},
                )
                response = connection.getresponse()
                header_elapsed = time.monotonic() - started
                body = response.read()
            self.assertTrue(upstream.idle_started.is_set())
            self.assertEqual(response.status, 200)
            self.assertLess(header_elapsed, 0.1)
            self.assertEqual(upstream.stream_values, [True, False])
            self.assertIn(b": keep-alive\n\n", body)
            self.assertNotIn(b"discard", body)
            self.assertIn(b'"type":"response.completed"', body)
        finally:
            connection.close()
            upstream.idle_release.set()
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()

    def test_keepalive_never_splits_replayed_sse_event(self):
        class LargeStreamHandler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_POST(self):
                self.rfile.read(int(self.headers.get("Content-Length", "0")))
                body = (
                    b'data: {"type":"response.completed","response":'
                    b'{"status":"completed","error":null,"output":[],"padding":"'
                    + b"x" * (192 * 1024)
                    + b'"}}\n\n'
                )
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, _format, *_args):
                pass

        upstream = start_server(LargeStreamHandler, "Upstream")
        proxy = start_proxy(f"http://127.0.0.1:{upstream.server_port}")
        connection = http.client.HTTPConnection(
            "127.0.0.1", proxy.server_port, timeout=3
        )
        try:
            with patch("relay_http.STREAM_HEARTBEAT_INTERVAL", 0):
                connection.request(
                    "POST",
                    "/v1/responses",
                    b'{"model":"gpt-test","stream":true}',
                    {"Content-Type": "application/json"},
                )
                response = connection.getresponse()
                body = response.read()
            data_lines = [line[6:] for line in body.splitlines() if line.startswith(b"data: ")]
            self.assertEqual(len(data_lines), 1)
            self.assertEqual(json.loads(data_lines[0])["type"], "response.completed")
        finally:
            connection.close()
            for server in (proxy, upstream):
                server.shutdown()
                server.server_close()


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
                first.set_tunnel_provider("echo")
                first.set_tunnel_allowed_models(("gpt-private",))
                first.set_relay_allowed_models(("gpt-private",))
                first.set_tunnel_provider("local")
                first.set_tunnel_allowed_models(("claude-private",))
                first.set_relay_allowed_models(("claude-private",))
                first.set_tunnel_provider("echo")
                first.set_tunnel_rpm_per_ip(45)
                first.set_tunnel_context_limit_kib(2048)
                profile = "v1.23456." + ("c" * 48)
                first.set_tunnel_publisher_profile(profile)
                token = first.tunnel_access_token()
                self.assertEqual(first.tunnel_rpm_per_ip(), 45)
                self.assertEqual(first.tunnel_context_limit_kib(), 2048)
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
                self.assertEqual(second.tunnel_provider_id(), "echo")
                self.assertEqual(
                    second.tunnel_allowed_models(),
                    ("gpt-private", "claude-private"),
                )
                self.assertEqual(
                    second.tunnel_model_routes(),
                    (
                        {"model": "gpt-private", "provider_id": "echo"},
                        {"model": "claude-private", "provider_id": "local"},
                    ),
                )
                self.assertEqual(
                    second.relay_model_routes(),
                    (
                        {"model": "gpt-private", "provider_id": "echo"},
                        {"model": "claude-private", "provider_id": "local"},
                    ),
                )
                self.assertEqual(second.tunnel_snapshot()["allowed_count"], 2)
                self.assertEqual(second.tunnel_rpm_per_ip(), 45)
                self.assertEqual(second.tunnel_context_limit_kib(), 2048)
                self.assertEqual(second.tunnel_publisher_profile(), profile)
                self.assertEqual(second.tunnel_snapshot()["rpm_per_ip"], 45)
                self.assertEqual(second.tunnel_snapshot()["context_limit_kib"], 2048)
            finally:
                second.server_close()

    def test_missing_tunnel_provider_migrates_to_saved_active_provider(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            config_path = Path(temporary_directory) / "config.dpapi"
            registry = relay.ProviderRegistry.defaults("", 0)
            try:
                registry.select("echo")
                value = registry.export_config()
            finally:
                registry.close()
            value["tunnel"] = {
                "allowed_models": ["gpt-migrated"],
                "access_token": "m" * 32,
                "rpm_per_ip": 15,
                "context_limit_kib": 0,
                "publisher_profile": relay_tunnel.DEFAULT_PUBLISHER_PROFILE,
            }
            relay_http.ConfigStore(config_path).save(value)

            server = relay.RelayServer(
                ("127.0.0.1", 0),
                relay.RelayHandler,
                echo_api_key="",
                config_path=config_path,
                history=False,
            )
            try:
                self.assertEqual(server.registry.active().id, "echo")
                self.assertEqual(server.tunnel_provider_id(), "echo")
                self.assertEqual(server.tunnel_allowed_models(), ("gpt-migrated",))
            finally:
                server.server_close()

            normalized = relay_http.ConfigStore(config_path).load()
            self.assertEqual(normalized["tunnel"]["provider_id"], "echo")

            normalized["tunnel"]["provider_id"] = "missing-provider"
            relay_http.ConfigStore(config_path).save(normalized)
            with self.assertRaisesRegex(ValueError, "Saved tunnel settings"):
                relay.RelayServer(
                    ("127.0.0.1", 0),
                    relay.RelayHandler,
                    echo_api_key="",
                    config_path=config_path,
                    history=False,
                )

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
                environment_id = first.provider_keys("echo")[0]["id"]
                first.update_provider_key("echo", environment_id, 47)
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
                    [key["rpm"] for key in second.provider_keys("echo")], [47, 120]
                )
                self.assertEqual(
                    [key["proxy"] for key in second.provider_keys("echo")],
                    ["Direct", "127.0.0.1:8888"],
                )
                environment_id = second.provider_keys("echo")[0]["id"]
                second.update_provider_key("echo", environment_id, 31)
                self.assertEqual(second.provider_keys("echo")[0]["rpm"], 31)
                with self.assertRaisesRegex(ValueError, "Direct"):
                    second.update_provider_key(
                        "echo",
                        environment_id,
                        31,
                        "http://127.0.0.1:9999",
                    )
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
            saved = relay_http.ConfigStore(config_path).load()
            echo = next(provider for provider in saved["providers"] if provider["id"] == "echo")
            self.assertEqual(echo["environment_key_rpm"], 31)
            self.assertNotIn("env-lite-a", json.dumps(saved))
            self.assertNotIn("env-lite-b", json.dumps(saved))

            third = relay.RelayServer(
                ("127.0.0.1", 0),
                relay.RelayHandler,
                echo_api_key="env-lite-c",
                config_path=config_path,
                history=False,
            )
            try:
                self.assertEqual(third.provider_keys("echo")[0]["rpm"], 31)
                self.assertEqual(third.provider_keys("echo")[0]["proxy"], "Direct")
            finally:
                third.server_close()
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
        context = {"value": 0}
        server.tunnel_context_limit_kib = lambda: context["value"]
        server.set_tunnel_context_limit_kib = lambda value: context.update(value=value)
        profile = {"value": "private-publisher-profile"}
        server.tunnel_publisher_profile = lambda: profile["value"]
        server.set_tunnel_publisher_profile = lambda value: profile.update(value=value)
        server.shared_tunnels = lambda: {
            "available": False,
            "revision": 0,
            "tunnels": [],
            "error": "Shared tunnel control unavailable",
        }
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
                    context["value"] = 0
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
                        await pilot.pause()
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
                        context_input = app.query_one("#tunnel-context-input", Input)
                        save_context = app.query_one("#tunnel-save-context", Button)
                        controls.extend((rpm_input, save_rpm, context_input, save_context))
                        self.assertTrue(all(control.region.width > 0 for control in controls))
                        self.assertEqual(
                            len({control.region.y for control in controls}),
                            2 if size[0] < 150 else 1,
                        )
                        self.assertEqual(rpm_input.value, "0")
                        self.assertEqual(str(rpm_input.border_title), "Per-IP RPM")
                        self.assertFalse(rpm_input.disabled)
                        self.assertFalse(save_rpm.disabled)
                        self.assertEqual(context_input.value, "0")
                        self.assertEqual(str(context_input.border_title), "Context KiB")
                        self.assertFalse(context_input.disabled)
                        self.assertFalse(save_context.disabled)
                        rpm_input.value = "37"
                        await pilot.click("#tunnel-save-rpm")
                        await pilot.pause()
                        self.assertEqual(rpm["value"], 37)
                        rpm_input.value = "0"
                        await pilot.click("#tunnel-save-rpm")
                        await pilot.pause()
                        self.assertEqual(rpm["value"], 0)
                        context_input.value = "2048"
                        await pilot.click("#tunnel-save-context")
                        await pilot.pause()
                        self.assertEqual(context["value"], 2048)
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
                        tunnel_state.update(
                            state="reconnecting",
                            url="",
                            error="Tunnel connection stopped",
                        )
                        app._refresh_tunnel()
                        self.assertIn(
                            "Reconnecting",
                            str(app.query_one("#tunnel-status", Static).content),
                        )
                        self.assertTrue(
                            app.query_one("#tunnel-start", Button).disabled
                        )
                        self.assertFalse(
                            app.query_one("#tunnel-stop", Button).disabled
                        )
                        self.assertTrue(profile_input.disabled)
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
                self.assertIn(
                    "1 queued · 0 retrying",
                    str(app.query_one("#metric-throughput", Static).content),
                )
                server.metrics.retry(
                    request_id, "echo", 400, 1, 0, False, 1, status=503
                )
                await pilot.pause(0.35)
                self.assertIn(
                    "0 queued · 1 retrying",
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
    def test_live_queue_time_stops_when_request_is_dispatched(self):
        with patch("relay_runtime.time.monotonic", return_value=10.0):
            metrics = relay.RelayMetrics()
            request_id = metrics.begin("echo", "EchoGate", "POST", "/v1/responses")
            metrics.request(request_id, "gpt-test", 10, False)
        with patch("relay_runtime.time.monotonic", return_value=12.0):
            queued = metrics.snapshot()["live"][0]
        self.assertEqual(queued["state"], "queued")
        self.assertEqual(queued["queue_ms"], 2000)

        metrics.dispatch(request_id, "echo", 2050)
        with patch("relay_runtime.time.monotonic", return_value=15.0):
            active = metrics.snapshot()["live"][0]
        self.assertEqual(active["state"], "active")
        self.assertEqual(active["queue_ms"], 2050)

    def test_retry_state_and_last_error_survive_the_next_dispatch(self):
        metrics = relay.RelayMetrics()
        request_id = metrics.begin("echo", "EchoGate", "POST", "/v1/responses")
        metrics.request(request_id, "gpt-test", 10, False)
        metrics.dispatch(request_id, "echo", 0)
        metrics.retry(
            request_id,
            "echo",
            1000,
            1,
            0,
            False,
            0.1,
            status=504,
            detail="Upstream timed out",
        )
        metrics.dispatch(request_id, "echo", 25)

        retrying = metrics.snapshot()["live"][0]
        self.assertEqual(retrying["state"], "retry")
        self.assertEqual(retrying["status"], 504)
        self.assertIn("Upstream timed out", retrying["error_detail"])

        metrics.response(request_id, "echo", 200, 1100, False)
        active = metrics.snapshot()["live"][0]
        self.assertEqual((active["state"], active["status"]), ("active", 200))
        self.assertEqual(active["error_detail"], "")

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

    def test_sse_inspector_resumes_after_oversized_image_event(self):
        inspector = relay.ResponseInspector("text/event-stream")
        inspector.feed(
            b'data: {"type":"response.image_generation_call.partial_image",'
            b'"partial_image_b64":"'
            + (b"A" * relay_http.MAX_INSPECT_BYTES)
        )
        with patch("relay_http.time.monotonic", return_value=77.0):
            inspector.feed(
                b'"}\n\n'
                b'data: {"type":"image_generation.completed"}\n\n'
            )
        self.assertEqual(inspector.terminal_at, 77.0)
        self.assertEqual(inspector.finish(), relay.TokenUsage())

        completed = relay.ResponseInspector(
            "text/event-stream",
            relay_http.MAX_SSE_EVENT_BYTES,
        )
        completed.feed(
            b'data: {"response":{"status":"completed","error":null,'
            b'"output":[{"content":"'
            + (b"A" * relay_http.MAX_INSPECT_BYTES)
            + b'"}]},"type":"response.completed"}\n\n'
        )
        self.assertEqual(completed.terminal_event, "response.completed")

        malformed = relay.ResponseInspector(
            "text/event-stream",
            relay_http.MAX_SSE_EVENT_BYTES,
        )
        malformed.feed(b'data: {"type":"response.completed","response":\n\n')
        self.assertEqual(malformed.terminal_event, "")

        incomplete = relay.ResponseInspector("text/event-stream")
        incomplete.feed(
            b'data: {"type":"response.completed","response":'
            b'{"status":"incomplete","error":null,"output":[]}}\n\n'
        )
        self.assertEqual(incomplete.terminal_event, "response.incomplete")

        contradictory = relay.ResponseInspector("text/event-stream")
        contradictory.feed(
            b'data: {"type":"response.completed","response":'
            b'{"status":"completed","error":null,"output":[],'
            b'"incomplete_details":{"reason":"max_output_tokens"}}}\n\n'
        )
        self.assertEqual(contradictory.terminal_event, "response.incomplete")

        multiline = relay.ResponseInspector("text/event-stream")
        multiline.feed(
            b"data: {\n"
            b'data: "type":"response.completed",\n'
            b'data: "response":{"status":"completed","error":null,"output":[]}\n'
            b"data: }\n\n"
        )
        self.assertEqual(multiline.terminal_event, "response.completed")

        for invalid in (
            b'data: {"type":"response.completed","type":"response.failed"}\n\n',
            b'data: {"type":"response.completed","bad":NaN}\n\n',
        ):
            strict = relay.ResponseInspector("text/event-stream")
            strict.feed(invalid)
            self.assertEqual(strict.terminal_event, "")

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
    def test_tunnel_history_is_separate_sanitized_and_restorable(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            path = Path(temporary_directory) / "tunnel_history.db"
            store = TunnelHistoryStore(path)
            self.assertTrue(
                store.record(
                    {
                        "timestamp": 123.0,
                        "ip": "203.0.113.42",
                        "method": "POST",
                        "path": "/v1/responses",
                        "model": "public-model",
                        "status": 200,
                        "state": "success",
                        "latency_ms": 12.5,
                        "request_bytes": 100,
                        "response_bytes": 200,
                        "provider": "must-not-persist",
                        "api_key": "secret-key-must-not-persist",
                        "body": "private prompt must not persist",
                    }
                )
            )
            recent = store.recent()
            store.close()
            self.assertEqual(len(recent), 1)
            self.assertEqual(recent[0]["ip"], "203.0.113.42")
            self.assertEqual(recent[0]["model"], "public-model")
            raw = path.read_bytes()
            for private in (
                b"must-not-persist",
                b"secret-key-must-not-persist",
                b"private prompt must not persist",
            ):
                self.assertNotIn(private, raw)

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


class KeyPoolTest(unittest.TestCase):
    def test_model_block_and_429_cooldown_do_not_hold_an_available_key(self):
        pool = relay_runtime.KeyPool(
            (("lite-key", 0, ""), ("pro-key", 0, ""))
        )
        threads = []
        try:
            lite, _ = pool.acquire(lambda: False, "blocked-model")
            pool.defer(
                lite,
                60,
                rate_limited=False,
                block_model=True,
                model="blocked-model",
            )
            pro, _ = pool.acquire(lambda: False, "blocked-model")
            pool.defer(pro, 0.5, rate_limited=True)

            started = time.monotonic()
            results = {}

            def acquire(name, model):
                attempt, waited = pool.acquire(lambda: False, model)
                results[name] = (
                    attempt.api_key,
                    time.monotonic() - started,
                    waited,
                )

            blocked = threading.Thread(
                target=acquire, args=("blocked", "blocked-model"), daemon=True
            )
            blocked.start()
            threads.append(blocked)
            deadline = time.monotonic() + 1
            while pool.snapshot()["queued"] != 1 and time.monotonic() < deadline:
                time.sleep(0.005)
            self.assertEqual(pool.snapshot()["queued"], 1)

            compatible = threading.Thread(
                target=acquire, args=("compatible", "other-model"), daemon=True
            )
            compatible.start()
            threads.append(compatible)
            compatible.join(0.2)

            self.assertFalse(compatible.is_alive())
            self.assertTrue(blocked.is_alive())
            self.assertEqual(results["compatible"][0], "lite-key")
            self.assertLess(results["compatible"][1], 0.2)

            blocked.join(1)
            self.assertFalse(blocked.is_alive())
            self.assertEqual(results["blocked"][0], "pro-key")
            self.assertGreaterEqual(results["blocked"][1], 0.4)
        finally:
            pool.close()
            for thread in threads:
                thread.join(1)

    def test_rpm_change_wakes_waiters_in_fifo_order(self):
        pool = relay_runtime.KeyPool((("key", 1, ""),))
        threads = []
        try:
            pool.acquire(lambda: False, "model")
            order = []
            admitted_at = []

            def acquire(index):
                pool.acquire(lambda: False, "model")
                order.append(index)
                admitted_at.append(time.monotonic())

            for index in range(2):
                thread = threading.Thread(target=acquire, args=(index,), daemon=True)
                thread.start()
                threads.append(thread)
                deadline = time.monotonic() + 1
                while (
                    pool.snapshot()["queued"] < index + 1
                    and time.monotonic() < deadline
                ):
                    time.sleep(0.005)
                self.assertEqual(pool.snapshot()["queued"], index + 1)

            pool.update(relay_runtime.key_fingerprint("key"), 600)
            for thread in threads:
                thread.join(1)

            self.assertTrue(all(not thread.is_alive() for thread in threads))
            self.assertEqual(order, [0, 1])
            self.assertGreaterEqual(admitted_at[1] - admitted_at[0], 0.07)
            self.assertEqual(pool.snapshot()["queued"], 0)
        finally:
            pool.close()
            for thread in threads:
                thread.join(1)


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

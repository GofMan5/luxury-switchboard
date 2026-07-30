import http.client
import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

import relay_http
import relay_image_compat
from relay_runtime import ProviderRegistry


class ImageCompatTest(unittest.TestCase):
    def test_prepares_only_codex_json_generation(self):
        original = {
            "model": "gpt-image-2",
            "prompt": "  blue robot  ",
            "background": "auto",
            "quality": "high",
            "size": "1024x1024",
            "output_format": "webp",
        }
        target, body, payload = relay_image_compat.prepare_image_request(
            "POST", "/v1/images/generations?ignored=1", original
        )
        self.assertEqual(target, "/v1/responses")
        self.assertEqual(json.loads(body), payload)
        self.assertEqual(
            payload,
            {
                "model": "gpt-5.6-sol",
                "input": "blue robot",
                "tools": [
                    {
                        "type": "image_generation",
                        "action": "generate",
                        "background": "auto",
                        "quality": "high",
                        "size": "1024x1024",
                    }
                ],
                "stream": True,
                "store": False,
            },
        )
        self.assertEqual(original["prompt"], "  blue robot  ")
        for method, path, model in (
            ("GET", "/v1/images/generations", "gpt-image-2"),
            ("POST", "/v1/images/edits", "gpt-image-2"),
            ("POST", "/v1/images/generations", "other-image"),
        ):
            self.assertIsNone(
                relay_image_compat.prepare_image_request(
                    method, path, {"model": model, "prompt": "keep"}
                )
            )

    def test_rejects_invalid_codex_image_request(self):
        for prompt in (None, "", "   "):
            with self.subTest(prompt=prompt), self.assertRaises(
                relay_image_compat.InvalidImageRequest
            ):
                relay_image_compat.prepare_image_request(
                    "POST",
                    "/v1/images/generations",
                    {"model": "gpt-image-2", "prompt": prompt},
                )

    def test_adapts_completed_sse_and_json(self):
        sse = (
            b'data: {"type":"response.created","response":{"created_at":1780000000}}\n\n'
            b'data: {"type":"response.image_generation_call.partial_image",'
            b'"partial_image_b64":"YWJjZA=="}\n\n'
            b'data: {"type":"response.completed","response":{"status":"completed",'
            b'"error":null,"output":[{"type":"image_generation_call",'
            b'"result":"YQ=="}]}}\n\ndata: [DONE]\n\n'
        )
        self.assertEqual(
            json.loads(relay_image_compat.images_response(sse)),
            {"created": 1780000000, "data": [{"b64_json": "YQ=="}]},
        )
        partial_fallback = (
            b'data: {"type":"response.image_generation_call.partial_image",'
            b'"partial_image_b64":"YWJjZA=="}\n\n'
            b'data: {"type":"response.image_generation_call.partial_image",'
            b'"partial_image_b64":"YQ=="}\n\n'
            b'data: {"type":"response.completed","response":{"status":"completed",'
            b'"error":null,"output":[]}}\n\n'
        )
        self.assertEqual(
            json.loads(relay_image_compat.images_response(partial_fallback))["data"],
            [{"b64_json": "YQ=="}],
        )
        direct = json.dumps(
            {
                "created_at": 1780000001,
                "status": "completed",
                "error": None,
                "output": [
                    {"type": "image_generation_call", "result": "YWJjZA=="}
                ],
            }
        ).encode()
        self.assertEqual(
            json.loads(relay_image_compat.images_response(direct))["created"],
            1780000001,
        )

    def test_rejects_malformed_failed_and_ambiguous_responses(self):
        completed = (
            b'data: {"type":"response.completed","response":{"status":"completed",'
            b'"error":null,"output":[{"result":"YWJjZA=="}]}}\n\n'
        )
        for body in (
            b"not-sse",
            b'data: {"type":"response.image_generation_call.partial_image",'
            b'"partial_image_b64":"YWJjZA=="}\n\n',
            completed + completed,
            b'data: {"type":"response.failed","response":{"status":"failed"}}\n\n',
            b'{"status":"incomplete","output":[{"result":"YWJjZA=="}]}',
            b'{"status":"completed","output":[{"result":"not-base64"}]}',
        ):
            with self.subTest(body=body[:40]), self.assertRaises(
                relay_image_compat.InvalidImageResponse
            ):
                relay_image_compat.images_response(body)
        with patch("relay_image_compat.MAX_IMAGE_RESPONSE_BYTES", 3):
            with self.assertRaises(relay_image_compat.InvalidImageResponse):
                relay_image_compat.images_response(b"1234")

    def test_relay_rewrites_and_adapts_without_forwarding_actor_header(self):
        class Upstream(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                payload = json.loads(body)
                self.server.requests.append(
                    (self.path, dict(self.headers.items()), payload)
                )
                terminal = (
                    b'data: {"type":"response.completed","response":'
                    b'{"created_at":1780000002,"status":"completed","error":null,'
                    b'"output":[{"type":"image_generation_call",'
                    b'"result":"YWJjZA=="}]}}\n\n'
                )
                response = terminal * (2 if payload["input"] == "duplicate" else 1)
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Content-Length", str(len(response)))
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(response)

            def log_message(self, _format, *_args):
                pass

        upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        upstream.requests = []
        upstream_thread = threading.Thread(target=upstream.serve_forever, daemon=True)
        upstream_thread.start()
        spec = ProviderRegistry.make_spec(
            "Local",
            f"http://127.0.0.1:{upstream.server_port}",
            auth_mode="passthrough",
            provider_id="local",
        )
        proxy = relay_http.RelayServer(
            ("127.0.0.1", 0),
            relay_http.RelayHandler,
            registry=ProviderRegistry(((spec, ()),)),
            history=False,
        )
        proxy_thread = threading.Thread(target=proxy.serve_forever, daemon=True)
        proxy_thread.start()

        def request(prompt):
            connection = http.client.HTTPConnection(*proxy.server_address, timeout=3)
            connection.request(
                "POST",
                "/v1/images/generations",
                json.dumps(
                    {
                        "model": "gpt-image-2",
                        "prompt": prompt,
                        "quality": "high",
                    }
                ).encode(),
                {
                    "Content-Type": "application/json",
                    "X-OpenAI-Actor-Authorization": "must-not-forward",
                },
            )
            response = connection.getresponse()
            result = response.status, response.getheader("Content-Type"), response.read()
            connection.close()
            return result

        try:
            status, content_type, body = request("blue robot")
            self.assertEqual((status, content_type), (200, "application/json"))
            self.assertEqual(
                json.loads(body),
                {"created": 1780000002, "data": [{"b64_json": "YWJjZA=="}]},
            )
            path, headers, payload = upstream.requests[0]
            self.assertEqual(path, "/v1/responses")
            self.assertNotIn(
                "x-openai-actor-authorization",
                {name.casefold() for name in headers},
            )
            self.assertEqual(payload["model"], "gpt-5.6-sol")
            self.assertEqual(payload["tools"][0]["action"], "generate")
            self.assertIs(payload["stream"], True)
            self.assertIs(payload["store"], False)

            status, content_type, body = request("duplicate")
            self.assertEqual((status, content_type), (502, "application/json"))
            self.assertEqual(json.loads(body), {"error": "Image generation failed"})
            self.assertEqual(len(upstream.requests), 2)
        finally:
            proxy.shutdown()
            proxy.server_close()
            upstream.shutdown()
            upstream.server_close()
            proxy_thread.join(1)
            upstream_thread.join(1)


if __name__ == "__main__":
    unittest.main()

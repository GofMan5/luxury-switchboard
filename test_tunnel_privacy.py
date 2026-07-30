import base64
import codecs
import http.client
import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import unquote

from relay_tunnel import CLIENT_IP_HEADER, TunnelGateway


TOKEN = "public-test-token-000000000000000000000000000000000000"
MODEL = "test-model"
PRIVATE_MARKER = "EchoGate"


class _ProbeUpstream(BaseHTTPRequestHandler):
    def do_POST(self) -> None:
        payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        probe = payload.get("probe")
        if probe == "nested-sse":
            event = {
                "type": "response.output_item.added",
                "output": [
                    {
                        "type": "message",
                        "content": [{"type": "output_text", "text": "safe"}],
                        "routing_hint": PRIVATE_MARKER,
                    }
                ],
            }
            body = (
                f"data: {json.dumps(event)}\n\ndata: [DONE]\n\n"
            ).encode()
            content_type = "text/event-stream"
        else:
            response = {"model": MODEL}
            if "metadata" in payload:
                response["metadata"] = payload["metadata"]
            elif probe == "unsolicited":
                response["routing_hint"] = PRIVATE_MARKER
            elif probe == "nested-json":
                response["output"] = [
                    {
                        "type": "message",
                        "content": [{"type": "output_text", "text": "safe"}],
                        "routing_hint": PRIVATE_MARKER,
                    }
                ]
            else:
                output = payload.get("candidate")
                if payload.get("transform") == "percent4":
                    for _ in range(4):
                        output = unquote(output)
                elif payload.get("transform") == "base64x2":
                    for _ in range(2):
                        output = base64.b64decode(output).decode()
                elif payload.get("transform") == "reverse":
                    output = output[::-1]
                elif payload.get("transform") == "rot13":
                    output = codecs.decode(output, "rot_13")
                response["output"] = output
            body = json.dumps(response).encode()
            content_type = "application/json"
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format, *_args) -> None:
        pass


class TunnelPrivacyRegressionTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.upstream = ThreadingHTTPServer(("127.0.0.1", 0), _ProbeUpstream)
        cls.upstream_thread = threading.Thread(
            target=cls.upstream.serve_forever, daemon=True
        )
        cls.upstream_thread.start()
        cls.gateway = TunnelGateway(
            cls.upstream.server_address,
            TOKEN,
            (MODEL,),
            sensitive_markers=(PRIVATE_MARKER,),
        )
        cls.gateway_thread = threading.Thread(
            target=cls.gateway.serve_forever, daemon=True
        )
        cls.gateway_thread.start()

    @classmethod
    def tearDownClass(cls) -> None:
        cls.gateway.shutdown()
        cls.gateway.server_close()
        cls.gateway_thread.join(timeout=1)
        cls.upstream.shutdown()
        cls.upstream.server_close()
        cls.upstream_thread.join(timeout=1)

    def request(self, payload: dict) -> tuple[int, tuple[str, ...], bytes]:
        body = json.dumps({"model": MODEL, **payload}).encode()
        connection = http.client.HTTPConnection(*self.gateway.server_address, timeout=2)
        connection.request(
            "POST",
            "/v1/responses",
            body,
            {
                "Authorization": f"Bearer {TOKEN}",
                CLIENT_IP_HEADER: "203.0.113.20",
                "Content-Type": "application/json",
            },
        )
        response = connection.getresponse()
        result = (
            response.status,
            tuple(sorted(name.casefold() for name, _value in response.getheaders())),
            response.read(),
        )
        connection.close()
        return result

    def test_provider_marker_echo_has_no_membership_oracle(self) -> None:
        observations = []
        for candidate in (PRIVATE_MARKER, "OtherGate"):
            status, headers, body = self.request({"candidate": candidate})
            payload = json.loads(body)
            self.assertEqual(status, 200)
            self.assertEqual(payload, {"model": MODEL, "output": candidate})
            observations.append((status, headers, tuple(sorted(payload))))
        self.assertEqual(observations[0], observations[1])

        status, headers, body = self.request({"probe": "unsolicited"})
        self.assertEqual(
            (status, json.loads(body)),
            (502, {"error": "Upstream response rejected"}),
        )
        self.assertNotIn(PRIVATE_MARKER.casefold(), ("\n".join(headers)).casefold())
        self.assertNotIn(PRIVATE_MARKER.encode(), body)

    def test_provider_marker_in_nested_metadata_is_rejected(self) -> None:
        for probe in ("nested-json", "nested-sse"):
            with self.subTest(probe=probe):
                status, headers, body = self.request(
                    {"probe": probe, "stream": probe.endswith("sse")}
                )
                self.assertEqual(
                    (status, json.loads(body)),
                    (502, {"error": "Upstream response rejected"}),
                )
                self.assertNotIn(
                    PRIVATE_MARKER.casefold(), ("\n".join(headers)).casefold()
                )
                self.assertNotIn(PRIVATE_MARKER.encode(), body)

    def test_echoed_request_metadata_is_removed_without_a_marker_oracle(self) -> None:
        observations = []
        for candidate in (PRIVATE_MARKER, "OtherGate"):
            status, headers, body = self.request(
                {"metadata": {"probe": candidate}}
            )
            payload = json.loads(body)
            self.assertEqual((status, payload), (200, {"model": MODEL}))
            self.assertNotIn(PRIVATE_MARKER.encode(), body)
            observations.append((status, headers, payload))
        self.assertEqual(observations[0], observations[1])

    def test_arbitrary_model_transforms_are_not_a_provider_oracle(self) -> None:
        pairs = (
            (
                "percent4",
                "EchoGate".replace("E", "%25252545"),
                "OtherGate".replace("O", "%2525254F"),
            ),
            (
                "base64x2",
                base64.b64encode(base64.b64encode(b"EchoGate")).decode(),
                base64.b64encode(base64.b64encode(b"OtherGate")).decode(),
            ),
            ("reverse", "etaGohcE", "etaGrehtO"),
            ("rot13", "RpubTngr", "BgureTngr"),
        )
        for transform, private_candidate, other_candidate in pairs:
            observations = []
            for candidate, expected in (
                (private_candidate, PRIVATE_MARKER),
                (other_candidate, "OtherGate"),
            ):
                with self.subTest(transform=transform, expected=expected):
                    status, headers, body = self.request(
                        {"candidate": candidate, "transform": transform}
                    )
                    payload = json.loads(body)
                    self.assertEqual(status, 200)
                    self.assertEqual(payload["output"], expected)
                    observations.append(
                        (status, headers, tuple(sorted(payload)))
                    )
            self.assertEqual(observations[0], observations[1])


if __name__ == "__main__":
    unittest.main()

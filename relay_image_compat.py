"""Compatibility bridge for Codex ``gpt-image-2`` generation calls."""

from __future__ import annotations

import base64
import binascii
import json
import time
from urllib.parse import urlsplit


CODEX_IMAGE_MODEL = "gpt-image-2"
RESPONSES_IMAGE_MODEL = "gpt-5.6-sol"
MAX_IMAGE_RESPONSE_BYTES = 16 * 1024 * 1024


class InvalidImageRequest(ValueError):
    pass


class InvalidImageResponse(ValueError):
    pass


def prepare_image_request(method: str, target: str, payload) -> tuple[str, bytes, dict] | None:
    """Rewrite only the JSON Images API call used by Codex."""
    if (
        method != "POST"
        or urlsplit(target).path != "/v1/images/generations"
        or not isinstance(payload, dict)
        or payload.get("model") != CODEX_IMAGE_MODEL
    ):
        return None
    prompt = payload.get("prompt")
    if not isinstance(prompt, str) or not (prompt := prompt.strip()):
        raise InvalidImageRequest("Image generation requires a non-empty prompt")

    tool = {"type": "image_generation", "action": "generate"}
    for field in ("background", "quality", "size"):
        if field in payload and payload[field] is not None:
            tool[field] = payload[field]
    request = {
        "model": RESPONSES_IMAGE_MODEL,
        "input": prompt,
        "tools": [tool],
        "stream": True,
        "store": False,
    }
    try:
        body = json.dumps(request, separators=(",", ":"), allow_nan=False).encode()
    except (TypeError, ValueError) as error:
        raise InvalidImageRequest("Image generation options are invalid") from error
    return "/v1/responses", body, request


def images_response(body: bytes) -> bytes:
    """Convert a completed Responses image result into Images API JSON."""
    if len(body) > MAX_IMAGE_RESPONSE_BYTES:
        raise InvalidImageResponse("Image generation response is too large")
    image = None
    created = None
    try:
        value = json.loads(body)
    except (json.JSONDecodeError, UnicodeDecodeError, RecursionError):
        value = None

    try:
        if value is not None:
            if _response_failed(value):
                raise InvalidImageResponse("Image generation returned a terminal failure")
            final_image, partial_image = _image_candidates(value)
            image = final_image or partial_image
            created = _created_at(value)
        else:
            image, created = _sse_image(body)
    except RecursionError as error:
        raise InvalidImageResponse("Image generation response is malformed") from error

    if image is None:
        raise InvalidImageResponse("Image generation completed without an image")
    created = created if created is not None else max(0, int(time.time()))
    return json.dumps(
        {"created": created, "data": [{"b64_json": image}]},
        separators=(",", ":"),
    ).encode()


def _sse_image(body: bytes) -> tuple[str | None, int | None]:
    try:
        text = body.decode("utf-8")
    except UnicodeDecodeError as error:
        raise InvalidImageResponse("Image generation returned invalid SSE") from error
    parsed = False
    completed = 0
    failed = False
    final_image = None
    partial_image = None
    created = None
    for line in text.splitlines():
        if not line.startswith("data:"):
            continue
        data = line[5:].strip()
        if not data or data == "[DONE]":
            continue
        try:
            value = json.loads(data)
        except (json.JSONDecodeError, RecursionError):
            continue
        parsed = True
        failed |= _response_failed(value)
        if isinstance(value, dict) and value.get("type") == "response.completed":
            completed += 1
        candidate_final, candidate_partial = _image_candidates(value)
        if candidate_final is not None and (
            final_image is None or len(candidate_final) > len(final_image)
        ):
            final_image = candidate_final
        if candidate_partial is not None:
            partial_image = candidate_partial
        created = created if created is not None else _created_at(value)
    if not parsed:
        raise InvalidImageResponse("Image generation returned invalid SSE")
    if failed:
        raise InvalidImageResponse("Image generation returned a terminal failure")
    if completed != 1:
        raise InvalidImageResponse(
            f"Image generation returned {completed} successful terminal events"
        )
    return final_image or partial_image, created


def _response_failed(value) -> bool:
    if not isinstance(value, dict):
        return False
    response = value.get("response")
    response = response if isinstance(response, dict) else {}
    return (
        value.get("type")
        in {"response.failed", "response.incomplete", "response.cancelled", "error"}
        or value.get("status", response.get("status"))
        in {"failed", "incomplete", "cancelled"}
        or value.get("error", response.get("error")) is not None
    )


def _created_at(value) -> int | None:
    if not isinstance(value, dict):
        return None
    response = value.get("response")
    response = response if isinstance(response, dict) else {}
    for candidate in (
        value.get("created_at"),
        value.get("created"),
        response.get("created_at"),
        response.get("created"),
    ):
        if isinstance(candidate, int) and not isinstance(candidate, bool) and candidate >= 0:
            return candidate
    return None


def _image_candidates(value) -> tuple[str | None, str | None]:
    final = None
    partial = None
    pending = [value]
    while pending:
        current = pending.pop()
        if isinstance(current, dict):
            for key, child in current.items():
                if key in {"result", "b64_json"} and isinstance(child, str):
                    candidate = child.strip()
                    if (final is None or len(candidate) > len(final)) and _valid_image(
                        candidate
                    ):
                        final = candidate
                elif key in {"partial_image", "partial_image_b64"} and isinstance(
                    child, str
                ):
                    candidate = child.strip()
                    if (
                        partial is None or len(candidate) > len(partial)
                    ) and _valid_image(candidate):
                        partial = candidate
                pending.append(child)
        elif isinstance(current, list):
            pending.extend(current)
    return final, partial


def _valid_image(value: str) -> bool:
    try:
        return bool(base64.b64decode(value.encode("ascii"), validate=True))
    except (UnicodeEncodeError, binascii.Error, ValueError):
        return False

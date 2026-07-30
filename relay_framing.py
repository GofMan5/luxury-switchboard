"""Bounded HTTP request framing and multipart metadata parsing."""

from __future__ import annotations

import re
from dataclasses import dataclass
from email.message import Message


MAX_CHUNK_LINE_BYTES = 8 * 1024
MAX_TRAILER_BYTES = 16 * 1024
MAX_CHUNKS = 8192
MAX_CHUNK_FRAMING_BYTES = 1024 * 1024
MAX_PART_HEADER_BYTES = 16 * 1024
MAX_FORM_FIELD_BYTES = 1024 * 1024
MAX_FORM_TEXT_BYTES = 1024 * 1024
MAX_MULTIPART_PARTS = 256
_TOKEN = re.compile(rb"[!#$%&'*+.^_`|~0-9A-Za-z-]+\Z")
_HEX = re.compile(rb"[0-9A-Fa-f]+\Z")
_CHUNK_EXTENSIONS = re.compile(
    rb"(?:;[!#$%&'*+.^_`|~0-9A-Za-z-]+"
    rb"(?:=[!#$%&'*+.^_`|~0-9A-Za-z-]+)?)*\Z"
)
_BOUNDARY_BYTES = frozenset(
    b"0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ'()+_,-./:=? "
)
_FILE_FIELDS = frozenset({"image", "mask"})


class InvalidBodyFraming(ValueError):
    pass


class BodyTooLarge(InvalidBodyFraming):
    pass


@dataclass(frozen=True)
class BodyFraming:
    length: int | None
    chunked: bool = False


@dataclass(frozen=True)
class MultipartForm:
    fields: tuple[tuple[str, str], ...]

    def values(self, name: str) -> tuple[str, ...]:
        return tuple(value for field, value in self.fields if field == name)


def body_framing(headers, max_bytes: int) -> BodyFraming:
    """Validate CL/TE without consuming the request body."""

    transfer = headers.get_all("Transfer-Encoding", [])
    lengths = headers.get_all("Content-Length", [])
    if transfer and lengths:
        raise InvalidBodyFraming("Content-Length with Transfer-Encoding")
    if transfer:
        if len(transfer) != 1 or transfer[0].strip().casefold() != "chunked":
            raise InvalidBodyFraming("Unsupported Transfer-Encoding")
        return BodyFraming(None, chunked=True)
    if not lengths:
        return BodyFraming(0)
    if len(lengths) != 1:
        raise InvalidBodyFraming("Duplicate Content-Length")
    normalized = [value.strip() for value in lengths]
    value = normalized[0]
    if not value.isascii() or not value.isdecimal():
        raise InvalidBodyFraming("Invalid Content-Length")
    if len(value) > len(str(max_bytes)):
        raise BodyTooLarge("Request body is too large")
    length = int(value)
    if length > max_bytes:
        raise BodyTooLarge("Request body is too large")
    return BodyFraming(length)


def read_body(stream, framing: BodyFraming, max_bytes: int) -> bytes:
    if not framing.chunked:
        body = stream.read(framing.length or 0)
        if len(body) != (framing.length or 0):
            raise InvalidBodyFraming("Incomplete request body")
        return body

    result = bytearray()
    chunks = 0
    framing_bytes = 0
    while True:
        line = stream.readline(MAX_CHUNK_LINE_BYTES + 1)
        if not line or len(line) > MAX_CHUNK_LINE_BYTES or not line.endswith(b"\r\n"):
            raise InvalidBodyFraming("Invalid chunk size")
        size_token, separator, extensions = line[:-2].partition(b";")
        extension_bytes = b";" + extensions if separator else b""
        framing_bytes += len(line) + 2
        if (
            not size_token
            or len(size_token) > 16
            or _HEX.fullmatch(size_token) is None
            or _CHUNK_EXTENSIONS.fullmatch(extension_bytes) is None
            or framing_bytes > MAX_CHUNK_FRAMING_BYTES
        ):
            raise InvalidBodyFraming("Invalid chunk size")
        size = int(size_token, 16)
        if size:
            chunks += 1
            if chunks > MAX_CHUNKS:
                raise InvalidBodyFraming("Too many chunks")
        if size > max_bytes - len(result):
            raise BodyTooLarge("Request body is too large")
        if size == 0:
            _read_trailers(stream)
            return bytes(result)
        chunk = stream.read(size)
        if len(chunk) != size or stream.read(2) != b"\r\n":
            raise InvalidBodyFraming("Incomplete chunk")
        result.extend(chunk)


def _read_trailers(stream) -> None:
    total = 0
    while True:
        line = stream.readline(MAX_CHUNK_LINE_BYTES + 1)
        total += len(line)
        if (
            not line
            or len(line) > MAX_CHUNK_LINE_BYTES
            or total > MAX_TRAILER_BYTES
            or not line.endswith(b"\r\n")
        ):
            raise InvalidBodyFraming("Invalid chunk trailer")
        if line == b"\r\n":
            return
        name, separator, value = line[:-2].partition(b":")
        if (
            not separator
            or _TOKEN.fullmatch(name) is None
            or any(byte < 0x20 and byte != 0x09 or byte == 0x7F for byte in value)
        ):
            raise InvalidBodyFraming("Invalid chunk trailer")


def media_type(content_type: str) -> str:
    return content_type.partition(";")[0].strip().casefold()


def parse_multipart(content_type: str, body: bytes) -> MultipartForm:
    """Extract only non-file UTF-8 fields while preserving the raw body."""

    if "\r" in content_type or "\n" in content_type:
        raise InvalidBodyFraming("Invalid Content-Type")
    header = Message()
    header["Content-Type"] = content_type
    if header.get_content_type().casefold() != "multipart/form-data":
        raise InvalidBodyFraming("Invalid multipart Content-Type")
    parameters = header.get_params(header="content-type", unquote=True) or []
    parameter_names = [
        key.casefold()
        for key, _item in parameters[1:]
        if isinstance(key, str)
    ]
    if (
        parameter_names != ["boundary"]
        or len(parameters) != 2
        or not isinstance(parameters[1][1], str)
    ):
        raise InvalidBodyFraming("Ambiguous multipart boundary")
    boundary = header.get_boundary()
    if not isinstance(boundary, str):
        raise InvalidBodyFraming("Missing multipart boundary")
    try:
        boundary_bytes = boundary.encode("ascii")
    except UnicodeEncodeError as error:
        raise InvalidBodyFraming("Invalid multipart boundary") from error
    if (
        not 1 <= len(boundary_bytes) <= 70
        or any(byte not in _BOUNDARY_BYTES for byte in boundary_bytes)
        or boundary_bytes.endswith(b" ")
    ):
        raise InvalidBodyFraming("Invalid multipart boundary")

    delimiter = b"--" + boundary_bytes
    if not body.startswith(delimiter):
        raise InvalidBodyFraming("Invalid multipart body")
    position = len(delimiter)
    fields: list[tuple[str, str]] = []
    text_bytes = 0
    parts = 0
    while True:
        if body[position : position + 2] == b"--":
            tail = body[position + 2 :]
            if tail not in {b"", b"\r\n"}:
                raise InvalidBodyFraming("Invalid multipart epilogue")
            return MultipartForm(tuple(fields))
        if body[position : position + 2] != b"\r\n":
            raise InvalidBodyFraming("Invalid multipart boundary")
        position += 2
        parts += 1
        if parts > MAX_MULTIPART_PARTS:
            raise InvalidBodyFraming("Too many multipart parts")

        header_end = body.find(b"\r\n\r\n", position)
        if header_end < 0 or header_end - position > MAX_PART_HEADER_BYTES:
            raise InvalidBodyFraming("Invalid multipart headers")
        headers = _part_headers(body[position:header_end])
        data_start = header_end + 4
        boundary_start = _next_boundary(body, b"\r\n" + delimiter, data_start)
        if boundary_start < 0:
            raise InvalidBodyFraming("Missing multipart boundary")
        part = memoryview(body)[data_start:boundary_start]
        position = boundary_start + 2 + len(delimiter)

        dispositions = headers.get("content-disposition", ())
        if len(dispositions) != 1:
            raise InvalidBodyFraming("Invalid Content-Disposition")
        name, is_file = _form_disposition(dispositions[0])
        if is_file or name.casefold() in _FILE_FIELDS:
            continue
        if len(part) > MAX_FORM_FIELD_BYTES:
            raise InvalidBodyFraming("Multipart field is too large")
        text_bytes += len(part)
        if text_bytes > MAX_FORM_TEXT_BYTES:
            raise InvalidBodyFraming("Multipart text is too large")
        try:
            value = bytes(part).decode("utf-8")
        except UnicodeDecodeError as error:
            raise InvalidBodyFraming("Invalid multipart text") from error
        fields.append((name, value))


def _next_boundary(body: bytes, marker: bytes, start: int) -> int:
    position = start
    while True:
        found = body.find(marker, position)
        if found < 0:
            return -1
        suffix = found + len(marker)
        if body[suffix : suffix + 2] in {b"\r\n", b"--"}:
            return found
        position = found + 1


def _part_headers(block: bytes) -> dict[str, tuple[str, ...]]:
    values: dict[str, list[str]] = {}
    if not block:
        raise InvalidBodyFraming("Missing multipart headers")
    for line in block.split(b"\r\n"):
        if not line or line[:1] in b" \t":
            raise InvalidBodyFraming("Invalid multipart header")
        name, separator, value = line.partition(b":")
        if not separator or _TOKEN.fullmatch(name) is None:
            raise InvalidBodyFraming("Invalid multipart header")
        if any(byte < 0x20 and byte != 0x09 or byte == 0x7F for byte in value):
            raise InvalidBodyFraming("Invalid multipart header")
        key = name.decode("ascii").casefold()
        values.setdefault(key, []).append(value.decode("latin-1").strip())
    return {key: tuple(items) for key, items in values.items()}


def _form_disposition(value: str) -> tuple[str, bool]:
    message = Message()
    message["Content-Disposition"] = value
    if message.get_content_disposition() != "form-data":
        raise InvalidBodyFraming("Invalid Content-Disposition")
    parameters = message.get_params(header="content-disposition", unquote=True) or []
    names = [
        key.casefold()
        for key, _item in parameters[1:]
        if isinstance(key, str)
    ]
    if names.count("name") != 1 or names.count("filename") > 1:
        raise InvalidBodyFraming("Ambiguous Content-Disposition")
    name = message.get_param("name", header="content-disposition", unquote=True)
    filename = message.get_param(
        "filename", header="content-disposition", unquote=True
    )
    if (
        not isinstance(name, str)
        or not name
        or len(name) > 128
        or not name.isascii()
        or not name.isprintable()
    ):
        raise InvalidBodyFraming("Invalid multipart field name")
    return name, filename is not None

"""Current-user encrypted persistence for provider settings."""

from __future__ import annotations

import ctypes
import json
import os
import tempfile
from ctypes import wintypes
from pathlib import Path


MAGIC = b"PSCF\x01"
MAX_CONFIG_BYTES = 2 * 1024 * 1024
CRYPTPROTECT_UI_FORBIDDEN = 0x1


class ConfigError(RuntimeError):
    pass


def default_config_path() -> Path:
    root = Path(os.environ.get("LOCALAPPDATA", Path.home()))
    return root / "ProviderSwitchboard" / "config.v1.dpapi"


class _DataBlob(ctypes.Structure):
    _fields_ = (
        ("cbData", wintypes.DWORD),
        ("pbData", ctypes.POINTER(ctypes.c_char)),
    )


def _dpapi(data: bytes, *, protect: bool) -> bytes:
    if os.name != "nt":
        raise ConfigError("Encrypted settings are only available on Windows")
    source_buffer = ctypes.create_string_buffer(data, len(data))
    source = _DataBlob(
        len(data), ctypes.cast(source_buffer, ctypes.POINTER(ctypes.c_char))
    )
    result = _DataBlob()
    crypt32 = ctypes.WinDLL("crypt32", use_last_error=True)
    function = crypt32.CryptProtectData if protect else crypt32.CryptUnprotectData
    function.restype = wintypes.BOOL
    if protect:
        ok = function(
            ctypes.byref(source),
            "Provider Switchboard",
            None,
            None,
            None,
            CRYPTPROTECT_UI_FORBIDDEN,
            ctypes.byref(result),
        )
    else:
        ok = function(
            ctypes.byref(source),
            None,
            None,
            None,
            None,
            CRYPTPROTECT_UI_FORBIDDEN,
            ctypes.byref(result),
        )
    if not ok:
        raise ConfigError("Saved settings could not be decrypted" if not protect else "Settings could not be encrypted")
    try:
        return ctypes.string_at(result.pbData, result.cbData)
    finally:
        local_free = ctypes.WinDLL("kernel32", use_last_error=True).LocalFree
        local_free.argtypes = (ctypes.c_void_p,)
        local_free.restype = ctypes.c_void_p
        local_free(ctypes.cast(result.pbData, ctypes.c_void_p))


class ConfigStore:
    def __init__(self, path: str | Path | None = None) -> None:
        self.path = Path(path) if path else default_config_path()

    def load(self) -> dict | None:
        try:
            if not self.path.exists():
                return None
            if self.path.stat().st_size > MAX_CONFIG_BYTES * 2:
                raise ConfigError("Saved settings are too large")
            payload = self.path.read_bytes()
        except ConfigError:
            raise
        except OSError as error:
            raise ConfigError("Saved settings could not be read") from error
        if not payload.startswith(MAGIC):
            raise ConfigError("Saved settings have an unsupported format")
        clear = _dpapi(payload[len(MAGIC) :], protect=False)
        if len(clear) > MAX_CONFIG_BYTES:
            raise ConfigError("Saved settings are too large")
        try:
            value = json.loads(clear.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError, RecursionError) as error:
            raise ConfigError("Saved settings are invalid") from error
        if not isinstance(value, dict):
            raise ConfigError("Saved settings are invalid")
        return value

    def save(self, value: dict) -> None:
        try:
            clear = json.dumps(
                value, ensure_ascii=False, separators=(",", ":"), sort_keys=True
            ).encode("utf-8")
        except (TypeError, ValueError, RecursionError) as error:
            raise ConfigError("Settings could not be encoded") from error
        if len(clear) > MAX_CONFIG_BYTES:
            raise ConfigError("Settings are too large")
        payload = MAGIC + _dpapi(clear, protect=True)
        try:
            self.path.parent.mkdir(parents=True, exist_ok=True)
            descriptor, temporary = tempfile.mkstemp(
                prefix=f".{self.path.name}.", dir=self.path.parent
            )
            try:
                with os.fdopen(descriptor, "wb") as output:
                    output.write(payload)
                    output.flush()
                    os.fsync(output.fileno())
                os.chmod(temporary, 0o600)
                os.replace(temporary, self.path)
            except BaseException:
                try:
                    os.unlink(temporary)
                except OSError:
                    pass
                raise
        except ConfigError:
            raise
        except OSError as error:
            raise ConfigError("Settings could not be saved") from error

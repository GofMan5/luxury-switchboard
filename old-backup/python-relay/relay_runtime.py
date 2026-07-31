"""Provider registry, rate gates, and in-memory relay metrics."""

from __future__ import annotations

import hashlib
import ipaddress
import threading
import time
import uuid
from collections import deque
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from urllib.parse import SplitResult, urlsplit


class ClientDisconnected(Exception):
    pass


class RelayStopping(Exception):
    pass


class ModelTemporarilyUnavailable(Exception):
    pass


UNCHANGED = object()
AUTH_MODES = ("auto", "passthrough", "bearer", "x-api-key")
MODEL_REPROBE_SECONDS = 300
ECHO_UPSTREAM = "https://api.echogate.one/v1"
MOSCOW_TIMEZONE = timezone(timedelta(hours=3), "MSK")


def next_moscow_midnight(now: float | None = None) -> float:
    current = datetime.fromtimestamp(
        time.time() if now is None else now, MOSCOW_TIMEZONE
    )
    return (
        current.replace(hour=0, minute=0, second=0, microsecond=0)
        + timedelta(days=1)
    ).timestamp()


def normalize_api_key(value: str | None) -> str:
    value = (value or "").strip()
    if value.lower().startswith("bearer "):
        value = value[7:].strip()
    if len(value) > 512:
        raise ValueError("API key is too long")
    if any(ord(character) < 32 or ord(character) == 127 for character in value):
        raise ValueError("API key contains control characters")
    return value


def key_fingerprint(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()[:10]


def _clean_name(value: str) -> str:
    if not isinstance(value, str):
        raise ValueError("Provider name is required")
    value = "".join(character for character in value.strip() if character.isprintable())
    if not value:
        raise ValueError("Provider name is required")
    if len(value) > 48:
        raise ValueError("Provider name is too long")
    return value


def _clean_provider_id(value: str) -> str:
    if not isinstance(value, str) or not value or len(value) > 64:
        raise ValueError("Provider ID is invalid")
    if any(not (character.isalnum() or character in "-_") for character in value):
        raise ValueError("Provider ID is invalid")
    return value


def _clean_upstream(value: str) -> str:
    if not isinstance(value, str):
        raise ValueError("Provider URL must use http:// or https://")
    value = value.strip()
    if any(ord(character) <= 32 or ord(character) == 127 for character in value):
        raise ValueError("Provider URL contains invalid characters")
    parsed = urlsplit(value)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname:
        raise ValueError("Provider URL must use http:// or https://")
    try:
        port = parsed.port
    except ValueError as error:
        raise ValueError("Provider port is invalid") from error
    if port == 0:
        raise ValueError("Provider port is invalid")
    if parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError("Provider URL cannot contain credentials, query, or fragment")
    if parsed.scheme == "http":
        try:
            loopback = ipaddress.ip_address(parsed.hostname).is_loopback
        except ValueError:
            loopback = parsed.hostname.lower() == "localhost"
        if not loopback:
            raise ValueError("Remote providers must use HTTPS")
    return parsed.geturl().rstrip("/")


def _clean_proxy(value: str | None) -> str:
    value = (value or "").strip()
    if not value:
        return ""
    if len(value) > 2048 or any(
        ord(character) <= 32 or ord(character) == 127 for character in value
    ):
        raise ValueError("Proxy URL is invalid")
    parsed = urlsplit(value)
    if parsed.scheme != "http" or not parsed.hostname:
        raise ValueError("Proxy URL must use http://")
    try:
        port = parsed.port
    except ValueError as error:
        raise ValueError("Proxy port is invalid") from error
    if port == 0:
        raise ValueError("Proxy port is invalid")
    if parsed.path not in {"", "/"} or parsed.query or parsed.fragment:
        raise ValueError("Proxy URL cannot contain a path, query, or fragment")
    return parsed.geturl().rstrip("/")


def _proxy_label(value: str) -> str:
    if not value:
        return "Direct"
    parsed = urlsplit(value)
    host = f"[{parsed.hostname}]" if ":" in (parsed.hostname or "") else parsed.hostname
    return f"{host}:{parsed.port or 80}"


@dataclass(frozen=True, slots=True)
class ProviderSpec:
    id: str
    name: str
    upstream: str
    auth_mode: str = "auto"
    cache_1h: bool = False
    rpm: int = 0

    @property
    def parsed_upstream(self) -> SplitResult:
        return urlsplit(self.upstream)


@dataclass(frozen=True, slots=True)
class ProviderView:
    id: str
    name: str
    upstream: str
    auth_mode: str
    cache_1h: bool
    rpm: int
    queued: int
    cooldown_ms: float
    retries_429: int
    key_count: int
    key_rpm: int
    effective_rpm: int
    active: bool
    actual_rpm: int = 0


@dataclass(frozen=True, slots=True)
class RouteLease:
    runtime: "ProviderRuntime"
    spec: ProviderSpec

    def release(self) -> None:
        self.runtime.release()


@dataclass(frozen=True, slots=True)
class TokenUsage:
    input_tokens: int = 0
    output_tokens: int = 0
    cached_tokens: int = 0
    reasoning_tokens: int = 0
    total_tokens: int = 0
    context_tokens: int = 0

    def as_dict(self) -> dict[str, int]:
        return {
            "input_tokens": self.input_tokens,
            "output_tokens": self.output_tokens,
            "cached_tokens": self.cached_tokens,
            "reasoning_tokens": self.reasoning_tokens,
            "total_tokens": self.total_tokens,
            "context_tokens": self.context_tokens or self.input_tokens,
        }


class RateGate:
    """Strict FIFO pacing with interruptible waits and shared 429 cooldown."""

    def __init__(self, rpm: int = 0):
        self._validate_rpm(rpm)
        self._condition = threading.Condition()
        self._rpm = rpm
        self._waiters: deque[object] = deque()
        self._next_slot = time.monotonic()
        self._cooldown_until = 0.0
        self._closed = False
        self._retries_429 = 0

    @staticmethod
    def _validate_rpm(rpm: int) -> None:
        if not isinstance(rpm, int) or isinstance(rpm, bool) or rpm < 0:
            raise ValueError("RPM must be a non-negative integer")

    def set_rpm(self, rpm: int) -> None:
        self._validate_rpm(rpm)
        with self._condition:
            self._rpm = rpm
            self._next_slot = time.monotonic()
            self._condition.notify_all()

    def acquire(self, cancelled) -> float:
        ticket = object()
        started = time.monotonic()
        with self._condition:
            if self._closed:
                raise RelayStopping
            self._waiters.append(ticket)
            try:
                while True:
                    if self._closed:
                        raise RelayStopping
                    if cancelled():
                        raise ClientDisconnected
                    now = time.monotonic()
                    first = self._waiters and self._waiters[0] is ticket
                    eligible_at = max(self._next_slot, self._cooldown_until)
                    if first and now >= eligible_at:
                        self._waiters.popleft()
                        self._next_slot = now + (60 / self._rpm if self._rpm else 0)
                        self._condition.notify_all()
                        return (now - started) * 1000
                    delay = eligible_at - now if first else 0.25
                    self._condition.wait(timeout=min(0.25, max(0.01, delay)))
            finally:
                if ticket in self._waiters:
                    self._waiters.remove(ticket)
                    self._condition.notify_all()

    def defer(self, delay: float, *, rate_limited: bool = False) -> None:
        with self._condition:
            self._cooldown_until = max(
                self._cooldown_until, time.monotonic() + max(0.25, delay)
            )
            self._retries_429 += int(rate_limited)
            self._condition.notify_all()

    def snapshot(self) -> dict[str, int | float]:
        with self._condition:
            now = time.monotonic()
            return {
                "rpm": self._rpm,
                "queued": len(self._waiters),
                "cooldown_ms": max(0.0, (self._cooldown_until - now) * 1000),
                "retries_429": self._retries_429,
            }

    def close(self) -> None:
        with self._condition:
            self._closed = True
            self._condition.notify_all()


@dataclass(slots=True, eq=False)
class _KeySlot:
    api_key: str = field(repr=False)
    rpm: int = 0
    proxy_url: str = field(default="", repr=False)
    next_slot: float = 0.0
    cooldown_until: float = 0.0
    balance_cooldown_until: float = 0.0
    retries_429: int = 0
    starts: deque[float] = field(default_factory=deque, repr=False)
    blocked_models: dict[str, float] = field(default_factory=dict, repr=False)


@dataclass(frozen=True, slots=True)
class KeyAttempt:
    _slot: _KeySlot | None = field(repr=False)

    @property
    def api_key(self) -> str | None:
        return self._slot.api_key if self._slot else None

    @property
    def proxy_url(self) -> str:
        return self._slot.proxy_url if self._slot else ""


@dataclass(slots=True, eq=False)
class _KeyWaiter:
    model: str
    slot: _KeySlot | None = None


class KeyPool:
    """Priority key scheduler with per-key RPM and work-conserving FIFO."""

    def __init__(self, api_keys=()):
        self._condition = threading.Condition()
        self._slots: list[_KeySlot] = []
        self._waiters: deque[_KeyWaiter] = deque()
        self._closed = False
        self.replace(api_keys)

    @staticmethod
    def _config(value) -> tuple[str, int, str]:
        if isinstance(value, str):
            api_key, rpm, proxy_url = value, 0, ""
        elif isinstance(value, (tuple, list)) and len(value) == 2:
            api_key, rpm = value
            proxy_url = ""
        elif isinstance(value, (tuple, list)) and len(value) == 3:
            api_key, rpm, proxy_url = value
        elif isinstance(value, dict):
            api_key, rpm = value.get("key"), value.get("rpm", 0)
            proxy_url = value.get("proxy", "")
        else:
            raise ValueError("API key configuration is invalid")
        api_key = normalize_api_key(api_key)
        if not api_key:
            raise ValueError("API key is required")
        RateGate._validate_rpm(rpm)
        return api_key, rpm, _clean_proxy(proxy_url)

    def replace(self, api_keys) -> None:
        configs = [self._config(value) for value in api_keys]
        if len({key for key, _, _ in configs}) != len(configs):
            raise ValueError("API keys must be unique")
        with self._condition:
            self._slots = [_KeySlot(key, rpm, proxy_url) for key, rpm, proxy_url in configs]
            self._condition.notify_all()

    def acquire(self, cancelled, model: str = "") -> tuple[KeyAttempt, float]:
        started = time.monotonic()
        ticket = _KeyWaiter(model)
        with self._condition:
            if self._closed:
                raise RelayStopping
            if not self._slots:
                return KeyAttempt(None), 0.0
            self._waiters.append(ticket)
            try:
                while True:
                    if self._closed:
                        raise RelayStopping
                    if cancelled():
                        raise ClientDisconnected
                    if ticket.slot is not None:
                        return KeyAttempt(ticket.slot), (time.monotonic() - started) * 1000
                    if not self._slots:
                        self._waiters.remove(ticket)
                        return KeyAttempt(None), (time.monotonic() - started) * 1000
                    now = time.monotonic()
                    wall_now = time.time()
                    for slot in self._slots:
                        while slot.starts and slot.starts[0] <= now - 60:
                            slot.starts.popleft()
                        for blocked_model, until in tuple(slot.blocked_models.items()):
                            if until <= now:
                                slot.blocked_models.pop(blocked_model, None)
                        if slot.balance_cooldown_until <= wall_now:
                            slot.balance_cooldown_until = 0.0

                    if ticket.model and all(
                        slot.blocked_models.get(ticket.model, 0.0) > now
                        for slot in self._slots
                    ):
                        self._waiters.remove(ticket)
                        raise ModelTemporarilyUnavailable

                    def eligible_at(slot, requested_model):
                        return max(
                            slot.next_slot,
                            slot.cooldown_until,
                            slot.blocked_models.get(requested_model, 0.0),
                            now
                            + max(0.0, slot.balance_cooldown_until - wall_now),
                        )

                    first_ready = next(
                        (
                            (waiter, slot)
                            for waiter in self._waiters
                            for slot in self._slots
                            if now >= eligible_at(slot, waiter.model)
                        ),
                        None,
                    )
                    if first_ready is not None:
                        waiter, slot = first_ready
                        self._waiters.remove(waiter)
                        slot.next_slot = now + (60 / slot.rpm if slot.rpm else 0)
                        slot.starts.append(now)
                        waiter.slot = slot
                        self._condition.notify_all()
                        continue

                    eligible = min(
                        eligible_at(slot, waiter.model)
                        for waiter in self._waiters
                        for slot in self._slots
                    )
                    self._condition.wait(
                        timeout=min(0.25, max(0.01, eligible - now))
                    )
            finally:
                if ticket in self._waiters:
                    self._waiters.remove(ticket)
                    self._condition.notify_all()

    def defer(
        self,
        attempt: KeyAttempt,
        delay: float,
        *,
        rate_limited: bool,
        block_model: bool = False,
        model: str = "",
    ) -> bool:
        slot = attempt._slot
        if slot is None:
            return False
        with self._condition:
            now = time.monotonic()
            if block_model and model and model != "—":
                slot.blocked_models[model] = max(
                    slot.blocked_models.get(model, 0.0), now + MODEL_REPROBE_SECONDS
                )
            else:
                slot.cooldown_until = max(
                    slot.cooldown_until, now + max(0.25, delay)
                )
            slot.retries_429 += int(rate_limited)
            self._condition.notify_all()
        return True

    def defer_balance(self, attempt: KeyAttempt, *, rate_limited: bool) -> bool:
        slot = attempt._slot
        if slot is None:
            return False
        with self._condition:
            slot.balance_cooldown_until = max(
                slot.balance_cooldown_until, next_moscow_midnight()
            )
            slot.retries_429 += int(rate_limited)
            self._condition.notify_all()
        return True

    def add(self, api_key: str, rpm: int = 0, proxy_url: str = "") -> str:
        api_key, rpm, proxy_url = self._config((api_key, rpm, proxy_url))
        with self._condition:
            if any(slot.api_key == api_key for slot in self._slots):
                raise ValueError("This API key is already configured")
            self._slots.append(_KeySlot(api_key, rpm, proxy_url))
            self._condition.notify_all()
        return key_fingerprint(api_key)

    def remove(self, fingerprint: str) -> None:
        with self._condition:
            slots = [
                slot
                for slot in self._slots
                if key_fingerprint(slot.api_key) != fingerprint
            ]
            if len(slots) == len(self._slots):
                raise KeyError("API key not found")
            self._slots = slots
            self._condition.notify_all()

    def move(self, fingerprint: str, direction: int) -> int:
        if type(direction) is not int or direction not in (-1, 1):
            raise ValueError("Key direction must be -1 or 1")
        with self._condition:
            for index, slot in enumerate(self._slots):
                if key_fingerprint(slot.api_key) != fingerprint:
                    continue
                target = index + direction
                if 0 <= target < len(self._slots):
                    self._slots[index], self._slots[target] = (
                        self._slots[target],
                        self._slots[index],
                    )
                    self._condition.notify_all()
                    return target
                return index
        raise KeyError("API key not found")

    def reset_cooldown(self, fingerprint: str) -> None:
        with self._condition:
            for slot in self._slots:
                if key_fingerprint(slot.api_key) == fingerprint:
                    slot.cooldown_until = 0.0
                    slot.balance_cooldown_until = 0.0
                    slot.blocked_models.clear()
                    self._condition.notify_all()
                    return
        raise KeyError("API key not found")

    def update(self, fingerprint: str, rpm: int, proxy_url: str | None = None) -> None:
        RateGate._validate_rpm(rpm)
        next_proxy = _clean_proxy(proxy_url) if proxy_url is not None else None
        with self._condition:
            for slot in self._slots:
                if key_fingerprint(slot.api_key) == fingerprint:
                    slot.rpm = rpm
                    if next_proxy is not None:
                        slot.proxy_url = next_proxy
                    slot.next_slot = time.monotonic()
                    self._condition.notify_all()
                    return
        raise KeyError("API key not found")

    def views(self) -> tuple[dict[str, int | float | str], ...]:
        with self._condition:
            now = time.monotonic()
            wall_now = time.time()
            result = []
            for index, slot in enumerate(self._slots, 1):
                while slot.starts and slot.starts[0] <= now - 60:
                    slot.starts.popleft()
                fingerprint = key_fingerprint(slot.api_key)
                result.append(
                    {
                        "id": fingerprint,
                        "label": f"Key {index} · {fingerprint}",
                        "rpm": slot.rpm,
                        "proxy": _proxy_label(slot.proxy_url),
                        "actual_rpm": len(slot.starts),
                        "cooldown_ms": max(
                            0.0,
                            (slot.cooldown_until - now) * 1000,
                            (slot.balance_cooldown_until - wall_now) * 1000,
                        ),
                        "retries_429": slot.retries_429,
                        "blocked_models": sum(
                            until > now for until in slot.blocked_models.values()
                        ),
                    }
                )
            return tuple(result)

    def configs(self) -> tuple[tuple[str, int, str], ...]:
        with self._condition:
            return tuple(
                (slot.api_key, slot.rpm, slot.proxy_url) for slot in self._slots
            )

    def snapshot(self) -> dict[str, int | float]:
        with self._condition:
            now = time.monotonic()
            wall_now = time.time()
            cooldowns = [
                max(
                    0.0,
                    slot.cooldown_until - now,
                    slot.balance_cooldown_until - wall_now,
                )
                for slot in self._slots
            ]
            return {
                "key_count": len(self._slots),
                "rpm": (
                    sum(slot.rpm for slot in self._slots)
                    if self._slots and all(slot.rpm for slot in self._slots)
                    else 0
                ),
                "queued": len(self._waiters),
                "cooldown_ms": min(cooldowns, default=0.0) * 1000,
                "retries_429": sum(slot.retries_429 for slot in self._slots),
            }

    def close(self) -> None:
        with self._condition:
            self._closed = True
            self._condition.notify_all()


class ProviderRuntime:
    def __init__(self, spec: ProviderSpec, api_keys=()):
        self._lock = threading.Lock()
        self._spec = spec
        self._keys = KeyPool(api_keys)
        self._inflight = 0
        self._retired = False
        self._gate = RateGate(spec.rpm)

    @property
    def gate(self) -> RateGate:
        return self._gate

    def acquire(self) -> RouteLease:
        with self._lock:
            spec = self._spec
            self._inflight += 1
            return RouteLease(self, spec)

    def acquire_attempt(self, cancelled, model: str = "") -> tuple[KeyAttempt, float]:
        attempt, waited = self._keys.acquire(cancelled, model)
        waited += self._gate.acquire(cancelled)
        return attempt, waited

    def defer_attempt(
        self,
        attempt: KeyAttempt,
        delay: float,
        *,
        rate_limited: bool,
        block_model: bool = False,
        model: str = "",
    ) -> None:
        if not self._keys.defer(
            attempt,
            delay,
            rate_limited=rate_limited,
            block_model=block_model,
            model=model,
        ):
            self._gate.defer(delay, rate_limited=rate_limited)

    def defer_balance(self, attempt: KeyAttempt, *, rate_limited: bool) -> bool:
        return self._keys.defer_balance(attempt, rate_limited=rate_limited)

    def release(self) -> None:
        close = False
        with self._lock:
            self._inflight = max(0, self._inflight - 1)
            close = self._retired and self._inflight == 0
        if close:
            self._gate.close()
            self._keys.close()

    def retire(self) -> None:
        close = False
        with self._lock:
            self._retired = True
            close = self._inflight == 0
        if close:
            self._gate.close()
            self._keys.close()

    def update(self, spec: ProviderSpec, api_keys=UNCHANGED) -> None:
        with self._lock:
            self._spec = spec
        if api_keys is not UNCHANGED:
            self._keys.replace(api_keys)
        self._gate.set_rpm(spec.rpm)

    def add_key(self, api_key: str, rpm: int = 0, proxy_url: str = "") -> str:
        return self._keys.add(api_key, rpm, proxy_url)

    def remove_key(self, fingerprint: str) -> None:
        self._keys.remove(fingerprint)

    def move_key(self, fingerprint: str, direction: int) -> int:
        return self._keys.move(fingerprint, direction)

    def reset_key_cooldown(self, fingerprint: str) -> None:
        self._keys.reset_cooldown(fingerprint)

    def update_key(
        self, fingerprint: str, rpm: int, proxy_url: str | None = None
    ) -> None:
        self._keys.update(fingerprint, rpm, proxy_url)

    def key_views(self) -> tuple[dict[str, int | float | str], ...]:
        return self._keys.views()

    def config(self) -> tuple[ProviderSpec, tuple[tuple[str, int, str], ...]]:
        with self._lock:
            spec = self._spec
        return spec, self._keys.configs()

    def view(self, active: bool) -> ProviderView:
        with self._lock:
            spec = self._spec
        gate = self._gate.snapshot()
        keys = self._keys.snapshot()
        key_rpm = int(keys["rpm"])
        effective_rpm = (
            min(spec.rpm, key_rpm)
            if spec.rpm and key_rpm
            else spec.rpm or key_rpm
        )
        return ProviderView(
            id=spec.id,
            name=spec.name,
            upstream=spec.upstream,
            auth_mode=spec.auth_mode,
            cache_1h=spec.cache_1h,
            rpm=spec.rpm,
            queued=int(gate["queued"]) + int(keys["queued"]),
            cooldown_ms=max(float(gate["cooldown_ms"]), float(keys["cooldown_ms"])),
            retries_429=int(gate["retries_429"]) + int(keys["retries_429"]),
            key_count=int(keys["key_count"]),
            key_rpm=key_rpm,
            effective_rpm=effective_rpm,
            active=active,
        )

    def close(self) -> None:
        self._gate.close()
        self._keys.close()


class ProviderRegistry:
    def __init__(self, providers: tuple[tuple[ProviderSpec, tuple[str, ...]], ...]):
        if not providers:
            raise ValueError("At least one provider is required")
        self._lock = threading.Lock()
        self._providers = {
            spec.id: ProviderRuntime(spec, keys) for spec, keys in providers
        }
        self._active_id = providers[0][0].id
        self._retired: list[ProviderRuntime] = []

    @staticmethod
    def make_spec(
        name: str,
        upstream: str,
        rpm: int = 0,
        auth_mode: str = "auto",
        cache_1h: bool = False,
        provider_id: str | None = None,
    ) -> ProviderSpec:
        RateGate._validate_rpm(rpm)
        if auth_mode not in AUTH_MODES:
            raise ValueError("Unsupported authentication mode")
        return ProviderSpec(
            id=(
                _clean_provider_id(provider_id)
                if provider_id is not None
                else uuid.uuid4().hex[:12]
            ),
            name=_clean_name(name),
            upstream=_clean_upstream(upstream),
            auth_mode=auth_mode,
            cache_1h=bool(cache_1h),
            rpm=rpm,
        )

    @classmethod
    def defaults(cls, echo_api_key: str = "", echo_rpm: int = 0) -> "ProviderRegistry":
        local = cls.make_spec(
            "Local",
            "http://127.0.0.1:8799",
            auth_mode="passthrough",
            provider_id="local",
        )
        echo = cls.make_spec(
            "EchoGate",
            ECHO_UPSTREAM,
            rpm=echo_rpm,
            auth_mode="auto",
            cache_1h=True,
            provider_id="echo",
        )
        keys = ((normalize_api_key(echo_api_key), 30, ""),) if echo_api_key else ()
        return cls(((local, ()), (echo, keys)))

    @classmethod
    def from_config(cls, value: dict, echo_api_key: str = "") -> "ProviderRegistry":
        if (
            not isinstance(value, dict)
            or not isinstance(value.get("schema"), int)
            or isinstance(value.get("schema"), bool)
            or value.get("schema") != 1
        ):
            raise ValueError("Saved settings schema is unsupported")
        raw_providers = value.get("providers")
        if not isinstance(raw_providers, list) or not 1 <= len(raw_providers) <= 64:
            raise ValueError("Saved providers are invalid")
        active_id = value.get("active")
        if not isinstance(active_id, str):
            raise ValueError("Saved active provider is invalid")

        providers = []
        provider_ids: set[str] = set()
        provider_names: set[str] = set()
        environment_key = normalize_api_key(echo_api_key)
        for raw in raw_providers:
            if not isinstance(raw, dict):
                raise ValueError("Saved provider is invalid")
            provider_id = _clean_provider_id(raw.get("id"))
            cache_1h = raw.get("cache_1h", False)
            if not isinstance(cache_1h, bool):
                raise ValueError("Saved provider cache setting is invalid")
            spec = cls.make_spec(
                raw.get("name"),
                raw.get("upstream"),
                raw.get("rpm", 0),
                raw.get("auth_mode", "auto"),
                cache_1h,
                provider_id=provider_id,
            )
            folded_name = spec.name.casefold()
            if provider_id in provider_ids or folded_name in provider_names:
                raise ValueError("Saved providers must be unique")
            provider_ids.add(provider_id)
            provider_names.add(folded_name)

            raw_keys = raw.get("keys", [])
            if not isinstance(raw_keys, list) or len(raw_keys) > 256:
                raise ValueError("Saved provider keys are invalid")
            keys = [KeyPool._config(item) for item in raw_keys]
            if (
                provider_id == "echo"
                and spec.upstream == ECHO_UPSTREAM
                and environment_key
            ):
                environment_key_rpm = raw.get("environment_key_rpm", 30)
                RateGate._validate_rpm(environment_key_rpm)
                keys = [
                    (environment_key, environment_key_rpm, ""),
                    *(item for item in keys if item[0] != environment_key),
                ]
            if len({key for key, _rpm, _proxy in keys}) != len(keys):
                raise ValueError("Saved provider keys must be unique")
            providers.append((spec, tuple(keys)))
        if active_id not in provider_ids:
            raise ValueError("Saved active provider is invalid")
        registry = cls(tuple(providers))
        registry.select(active_id)
        return registry

    def export_config(self, excluded_echo_key: str = "") -> dict:
        excluded_echo_key = normalize_api_key(excluded_echo_key)
        with self._lock:
            active_id = self._active_id
            items = tuple(self._providers.items())
        providers = []
        for provider_id, runtime in items:
            spec, keys = runtime.config()
            environment_key_rpm = next(
                (
                    rpm
                    for key, rpm, _proxy in keys
                    if provider_id == "echo"
                    and spec.upstream == ECHO_UPSTREAM
                    and excluded_echo_key
                    and key == excluded_echo_key
                ),
                None,
            )
            provider = {
                "id": spec.id,
                "name": spec.name,
                "upstream": spec.upstream,
                "auth_mode": spec.auth_mode,
                "cache_1h": spec.cache_1h,
                "rpm": spec.rpm,
                "keys": [
                    {"key": key, "rpm": rpm, "proxy": proxy}
                    for key, rpm, proxy in keys
                    if not (
                        provider_id == "echo"
                        and spec.upstream == ECHO_UPSTREAM
                        and excluded_echo_key
                        and key == excluded_echo_key
                    )
                ],
            }
            if environment_key_rpm is not None:
                provider["environment_key_rpm"] = environment_key_rpm
            providers.append(provider)
        return {"schema": 1, "active": active_id, "providers": providers}

    def _runtime(self, provider_id: str) -> ProviderRuntime:
        try:
            return self._providers[provider_id]
        except KeyError as error:
            raise KeyError("Provider not found") from error

    def acquire_active(self) -> RouteLease:
        with self._lock:
            runtime = self._runtime(self._active_id)
            return runtime.acquire()

    def acquire(self, provider_id: str) -> RouteLease:
        with self._lock:
            return self._runtime(provider_id).acquire()

    def list(self) -> tuple[ProviderView, ...]:
        with self._lock:
            active_id = self._active_id
            items = tuple(self._providers.items())
        return tuple(runtime.view(provider_id == active_id) for provider_id, runtime in items)

    def active(self) -> ProviderView:
        return next(provider for provider in self.list() if provider.active)

    def select(self, provider: str | int) -> ProviderView:
        with self._lock:
            ids = tuple(self._providers)
            provider_id = ids[provider] if isinstance(provider, int) else provider
            self._runtime(provider_id)
            self._active_id = provider_id
        return self.active()

    def toggle(self) -> ProviderView:
        with self._lock:
            ids = tuple(self._providers)
            index = (ids.index(self._active_id) + 1) % len(ids)
            self._active_id = ids[index]
        return self.active()

    def add(
        self,
        name: str,
        upstream: str,
        rpm: int = 0,
        auth_mode: str = "auto",
        cache_1h: bool = False,
        api_keys=(),
    ) -> ProviderView:
        spec = self.make_spec(name, upstream, rpm, auth_mode, cache_1h)
        runtime = ProviderRuntime(spec, api_keys)
        with self._lock:
            if any(
                view.name.casefold() == spec.name.casefold() for view in self.list_unlocked()
            ):
                raise ValueError("Provider name already exists")
            self._providers[spec.id] = runtime
        return runtime.view(False)

    def list_unlocked(self) -> tuple[ProviderView, ...]:
        active_id = self._active_id
        return tuple(
            runtime.view(provider_id == active_id)
            for provider_id, runtime in self._providers.items()
        )

    def update(
        self,
        provider_id: str,
        *,
        name=UNCHANGED,
        upstream=UNCHANGED,
        rpm=UNCHANGED,
        auth_mode=UNCHANGED,
        cache_1h=UNCHANGED,
        api_keys=UNCHANGED,
    ) -> ProviderView:
        with self._lock:
            runtime = self._runtime(provider_id)
            current = runtime.view(provider_id == self._active_id)
            next_name = current.name if name is UNCHANGED else name
            if any(
                view.id != provider_id
                and view.name.casefold() == str(next_name).strip().casefold()
                for view in self.list_unlocked()
            ):
                raise ValueError("Provider name already exists")
            spec = self.make_spec(
                next_name,
                current.upstream if upstream is UNCHANGED else upstream,
                current.rpm if rpm is UNCHANGED else rpm,
                current.auth_mode if auth_mode is UNCHANGED else auth_mode,
                current.cache_1h if cache_1h is UNCHANGED else cache_1h,
                provider_id=provider_id,
            )
            if (
                provider_id == "echo"
                and current.upstream == ECHO_UPSTREAM
                and spec.upstream != ECHO_UPSTREAM
            ):
                raise ValueError(
                    "Built-in EchoGate URL is fixed; add another provider instead"
                )
            runtime.update(spec, api_keys)
            active = provider_id == self._active_id
        return runtime.view(active)

    def delete(self, provider_id: str) -> ProviderView:
        with self._lock:
            if len(self._providers) == 1:
                raise ValueError("The last provider cannot be deleted")
            runtime = self._runtime(provider_id)
            ids = tuple(self._providers)
            index = ids.index(provider_id)
            del self._providers[provider_id]
            self._retired.append(runtime)
            if self._active_id == provider_id:
                remaining = tuple(self._providers)
                self._active_id = remaining[min(index, len(remaining) - 1)]
        runtime.retire()
        return self.active()

    def add_key(
        self, provider_id: str, api_key: str, rpm: int = 0, proxy_url: str = ""
    ) -> str:
        with self._lock:
            runtime = self._runtime(provider_id)
        return runtime.add_key(api_key, rpm, proxy_url)

    def remove_key(self, provider_id: str, fingerprint: str) -> None:
        with self._lock:
            runtime = self._runtime(provider_id)
        runtime.remove_key(fingerprint)

    def move_key(self, provider_id: str, fingerprint: str, direction: int) -> int:
        with self._lock:
            runtime = self._runtime(provider_id)
        return runtime.move_key(fingerprint, direction)

    def reset_key_cooldown(self, provider_id: str, fingerprint: str) -> None:
        with self._lock:
            runtime = self._runtime(provider_id)
        runtime.reset_key_cooldown(fingerprint)

    def update_key(
        self,
        provider_id: str,
        fingerprint: str,
        rpm: int,
        proxy_url: str | None = None,
    ) -> None:
        with self._lock:
            runtime = self._runtime(provider_id)
        runtime.update_key(fingerprint, rpm, proxy_url)

    def key_views(
        self, provider_id: str
    ) -> tuple[dict[str, int | float | str], ...]:
        with self._lock:
            runtime = self._runtime(provider_id)
        return runtime.key_views()

    def close(self) -> None:
        with self._lock:
            runtimes = (*self._providers.values(), *self._retired)
        for runtime in runtimes:
            runtime.close()


class RelayMetrics:
    def __init__(self):
        self._lock = threading.Lock()
        self._started_at = time.monotonic()
        self._total = self._active = self._successes = self._errors = self._cancelled = 0
        self._retries = 0
        self._retries_429 = 0
        self._bytes_in = self._bytes_out = 0
        self._latencies: deque[float] = deque(maxlen=200)
        self._recent: deque[dict] = deque(maxlen=100)
        self._live: dict[int, dict] = {}
        self._provider_status: dict[str, dict] = {}
        self._request_times: deque[float] = deque()
        self._provider_request_times: dict[str, deque[float]] = {}
        self._sequence = 0
        self._request_sequence = 0

    def begin(self, provider_id: str, provider: str, method: str, path: str) -> int:
        with self._lock:
            now = time.monotonic()
            self._request_times.append(now)
            provider_times = self._provider_request_times.setdefault(
                provider_id, deque()
            )
            provider_times.append(now)
            self._total += 1
            self._active += 1
            self._request_sequence += 1
            request_id = self._request_sequence
            self._live[request_id] = {
                "_request_id": request_id,
                "_started_at": now,
                "timestamp": time.time(),
                "time": time.strftime("%H:%M:%S"),
                "provider_id": provider_id,
                "provider": provider,
                "method": method,
                "path": path,
                "model": "—",
                "status": None,
                "latency_ms": 0,
                "bytes_in": 0,
                "bytes_out": 0,
                "state": "reading",
                "cache_1h": False,
                "retries": 0,
                "retries_429": 0,
                "queue_ms": 0,
                "error_detail": "",
                "tokens_per_second": 0.0,
                **TokenUsage().as_dict(),
            }
            return request_id

    def restore_recent(self, events) -> None:
        """Seed the dashboard with sanitized persistent history, newest last."""
        with self._lock:
            for stored in reversed(tuple(events)):
                event = dict(stored)
                history_id = int(event.pop("id"))
                event["seq"] = -history_id
                event["time"] = time.strftime(
                    "%H:%M:%S", time.localtime(float(event["timestamp"]))
                )
                self._recent.append(event)

    def request(self, request_id: int, model: str, bytes_in: int, cache_extended: bool) -> None:
        with self._lock:
            event = self._live.get(request_id)
            if event:
                event.update(
                    model=model,
                    bytes_in=bytes_in,
                    state="queued",
                    cache_1h=cache_extended,
                    _queued_at=time.monotonic(),
                )

    def route(self, request_id: int, provider_id: str, provider: str) -> None:
        with self._lock:
            event = self._live.get(request_id)
            if not event or event["provider_id"] == provider_id:
                return
            old_times = self._provider_request_times.get(event["provider_id"])
            started = event.get("_started_at")
            if old_times is not None and started is not None:
                try:
                    old_times.remove(started)
                except ValueError:
                    pass
            self._provider_request_times.setdefault(provider_id, deque()).append(started)
            event.update(provider_id=provider_id, provider=provider)

    def dispatch(self, request_id: int, provider_id: str, queue_ms: float) -> None:
        """Mark a queued request as sent upstream before response headers arrive."""
        with self._lock:
            event = self._live.get(request_id)
            if event:
                event.pop("_queued_at", None)
                event["queue_ms"] = queue_ms
                if not event["retries"]:
                    event.update(status=None, state="active", error_detail="")
            current = self._provider_status.get(provider_id)
            if current is None or request_id >= current.get("_request_id", 0):
                self._provider_status[provider_id] = dict(event or {})

    def response(
        self,
        request_id: int,
        provider_id: str,
        status: int,
        latency_ms: float,
        cache_extended: bool,
        generation_started_at: float | None = None,
    ) -> None:
        with self._lock:
            self._latencies.append(latency_ms)
            event = self._live.get(request_id)
            if event:
                event.update(
                    status=status,
                    latency_ms=latency_ms,
                    state="active",
                    cache_1h=cache_extended,
                    error_detail="",
                    _generation_started_at=(
                        generation_started_at
                        if generation_started_at is not None
                        else time.monotonic()
                    ),
                )
            current = self._provider_status.get(provider_id)
            if current is None or request_id >= current.get("_request_id", 0):
                self._provider_status[provider_id] = dict(event or {})

    def retry(
        self,
        request_id: int,
        provider_id: str,
        elapsed_ms: float,
        retry_number: int,
        retry_429_count: int,
        cache_extended: bool,
        delay: float,
        status: int | None = None,
        detail: str = "Retrying provider request",
    ) -> None:
        with self._lock:
            self._retries += 1
            self._retries_429 += int(status == 429)
            event = self._live.get(request_id)
            if event:
                event.update(
                    status=status,
                    latency_ms=elapsed_ms,
                    state="retry",
                    cache_1h=cache_extended,
                    retries=retry_number,
                    retries_429=retry_429_count,
                    error_detail=f"{detail} · retrying in {delay:.1f}s",
                )
            current = self._provider_status.get(provider_id)
            if current is None or request_id >= current.get("_request_id", 0):
                self._provider_status[provider_id] = dict(event or {})

    def add_bytes(self, bytes_in: int = 0, bytes_out: int = 0) -> None:
        with self._lock:
            self._bytes_in += bytes_in
            self._bytes_out += bytes_out

    def progress(self, request_id: int, bytes_out: int) -> None:
        with self._lock:
            event = self._live.get(request_id)
            if event:
                event["bytes_out"] = bytes_out

    def tokens(
        self, request_id: int, usage: TokenUsage, terminal_at: float | None = None
    ) -> None:
        with self._lock:
            event = self._live.get(request_id)
            if not event:
                return
            event.update(usage.as_dict())
            started = event.get("_generation_started_at")
            if (
                started
                and terminal_at is not None
                and "_usage_final_at" not in event
                and usage.output_tokens
            ):
                event["_usage_final_at"] = terminal_at
                event["tokens_per_second"] = usage.output_tokens / max(
                    0.001, terminal_at - started
                )

    def set_detail(self, request_id: int, detail: str) -> None:
        with self._lock:
            event = self._live.get(request_id)
            if event:
                event["error_detail"] = detail[:240]

    def finish(
        self,
        request_id: int,
        status: int | None,
        elapsed_ms: float,
        bytes_out: int,
        usage: TokenUsage,
        *,
        retries: int = 0,
        retries_429: int = 0,
        queue_ms: float = 0,
        failed: bool = False,
        cancelled: bool = False,
        error_detail: str = "",
        response_observed: bool = False,
    ) -> dict:
        synthetic_cancel = (
            cancelled
            and status == 503
            and error_detail == "Provider switched"
        )
        is_error = failed or (status is None and not cancelled) or (
            status is not None and status >= 400 and not synthetic_cancel
        )
        state = "error" if is_error else "cancelled" if cancelled else "ok"
        with self._lock:
            live = self._live.pop(request_id)
            token_speed = float(live.get("tokens_per_second", 0.0))
            if not token_speed and usage.output_tokens:
                finished_at = time.monotonic()
                generation_seconds = max(
                    0.001,
                    finished_at
                    - live.get(
                        "_generation_started_at",
                        live.get("_started_at", finished_at),
                    ),
                )
                token_speed = usage.output_tokens / generation_seconds
            self._active = max(0, self._active - 1)
            self._successes += int(not is_error and not cancelled)
            self._errors += int(is_error)
            self._cancelled += int(cancelled)
            if status is not None and not response_observed:
                self._latencies.append(elapsed_ms)
            self._sequence += 1
            event = {
                **{key: value for key, value in live.items() if not key.startswith("_")},
                "_request_id": request_id,
                "seq": self._sequence,
                "status": status,
                "latency_ms": elapsed_ms,
                "bytes_out": bytes_out,
                "state": state,
                "retries": retries,
                "retries_429": retries_429,
                "queue_ms": queue_ms,
                "error_detail": (
                    "" if state == "ok" else error_detail or live.get("error_detail", "")
                ),
                "tokens_per_second": token_speed,
                **usage.as_dict(),
            }
            self._recent.append(event)
            provider_id = event["provider_id"]
            current = self._provider_status.get(provider_id)
            if (status is not None or failed) and (
                current is None or request_id >= current.get("_request_id", 0)
            ):
                self._provider_status[provider_id] = event
            return dict(event)

    def provider_actual_rpm(self) -> dict[str, int]:
        with self._lock:
            cutoff = time.monotonic() - 60
            result = {}
            for provider_id, request_times in self._provider_request_times.items():
                while request_times and request_times[0] <= cutoff:
                    request_times.popleft()
                result[provider_id] = len(request_times)
            return result

    def snapshot(self) -> dict:
        with self._lock:
            now = time.monotonic()
            while self._request_times and self._request_times[0] <= now - 60:
                self._request_times.popleft()
            provider_actual_rpm = {}
            for provider_id, request_times in self._provider_request_times.items():
                while request_times and request_times[0] <= now - 60:
                    request_times.popleft()
                provider_actual_rpm[provider_id] = len(request_times)
            latencies = list(self._latencies)
            live = []
            for event in self._live.values():
                visible = dict(event)
                visible["latency_ms"] = (now - visible.pop("_started_at")) * 1000
                queued_at = visible.pop("_queued_at", None)
                if queued_at is not None:
                    visible["queue_ms"] = (now - queued_at) * 1000
                visible.pop("_generation_started_at", None)
                visible.pop("_usage_final_at", None)
                live.append(visible)
            ordered = sorted(latencies)
            return {
                "uptime": now - self._started_at,
                "total": self._total,
                "active": self._active,
                "successes": self._successes,
                "errors": self._errors,
                "cancelled": self._cancelled,
                "actual_rpm": len(self._request_times),
                "provider_actual_rpm": provider_actual_rpm,
                "retries": self._retries,
                "retries_429": self._retries_429,
                "bytes_in": self._bytes_in,
                "bytes_out": self._bytes_out,
                "average_ms": sum(latencies) / max(1, len(latencies)),
                "p95_ms": ordered[
                    min(len(ordered) - 1, int(len(ordered) * 0.95))
                ] if ordered else 0,
                "recent": list(self._recent),
                "live": live,
                "provider_status": dict(self._provider_status),
            }

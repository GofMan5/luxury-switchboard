"""Persistent sanitized request history and period statistics."""

from __future__ import annotations

import os
import re
import sqlite3
import threading
import time
from pathlib import Path


PERIODS = {"24h": 24, "48h": 48, "72h": 72, "all": None}

_MIGRATIONS = {
    "timestamp": "REAL NOT NULL DEFAULT 0",
    "provider_id": "TEXT NOT NULL DEFAULT ''",
    "provider": "TEXT NOT NULL DEFAULT 'Unknown'",
    "model": "TEXT NOT NULL DEFAULT '—'",
    "method": "TEXT NOT NULL DEFAULT ''",
    "path": "TEXT NOT NULL DEFAULT ''",
    "status": "INTEGER",
    "state": "TEXT NOT NULL DEFAULT 'error'",
    "cache_1h": "INTEGER NOT NULL DEFAULT 0",
    "latency_ms": "REAL NOT NULL DEFAULT 0",
    "queue_ms": "REAL NOT NULL DEFAULT 0",
    "retries": "INTEGER NOT NULL DEFAULT 0",
    "retries_429": "INTEGER NOT NULL DEFAULT 0",
    "bytes_in": "INTEGER NOT NULL DEFAULT 0",
    "bytes_out": "INTEGER NOT NULL DEFAULT 0",
    "input_tokens": "INTEGER NOT NULL DEFAULT 0",
    "context_tokens": "INTEGER NOT NULL DEFAULT 0",
    "output_tokens": "INTEGER NOT NULL DEFAULT 0",
    "cached_tokens": "INTEGER NOT NULL DEFAULT 0",
    "reasoning_tokens": "INTEGER NOT NULL DEFAULT 0",
    "total_tokens": "INTEGER NOT NULL DEFAULT 0",
    "tokens_per_second": "REAL NOT NULL DEFAULT 0",
    "error_detail": "TEXT NOT NULL DEFAULT ''",
}

_SAFE_ERROR_DETAILS = {
    "Chunked request bodies are not supported",
    "Client disconnected",
    "Conflicting Content-Length headers",
    "Invalid Content-Length",
    "Provider closed the response early",
    "Provider connection failed",
    "Provider did not respond before the timeout",
    "Provider hostname could not be resolved",
    "Provider is rate limited or overloaded",
    "Provider refused the connection",
    "Provider rejected authentication",
    "Provider rejected the request",
    "Provider rejected the request size",
    "Provider request timed out",
    "Provider reset the connection",
    "Provider returned a server error",
    "Provider returned an invalid HTTP response",
    "Provider route or model was not found",
    "Provider TLS handshake failed",
    "Relay stopped",
    "Request body is too large",
}
_RETRY_SUFFIX = re.compile(r"^ · retrying in \d+(?:\.\d)?s$")


def _safe_error_detail(value) -> str:
    if not isinstance(value, str):
        return ""
    detail = "".join(character for character in value if character.isprintable())[:240]
    base, marker, suffix = detail.partition(" · retrying in ")
    safe_base = base in _SAFE_ERROR_DETAILS or (
        base.startswith("Provider returned HTTP ")
        and base.removeprefix("Provider returned HTTP ").isdecimal()
    )
    if not safe_base:
        return ""
    if not marker:
        return base
    retry_suffix = marker + suffix
    return detail if _RETRY_SUFFIX.fullmatch(retry_suffix) else base


def default_history_path() -> Path:
    root = Path(os.environ.get("LOCALAPPDATA", Path.home()))
    return root / "ProviderSwitchboard" / "history.db"


class HistoryStore:
    def __init__(self, path: str | Path | None = None):
        self.path = Path(path) if path else default_history_path()
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self._lock = threading.Lock()
        self._closed = False
        self._connection = None
        try:
            self._connection = sqlite3.connect(
                self.path, timeout=5, check_same_thread=False, isolation_level=None
            )
            self._connection.row_factory = sqlite3.Row
            self._connection.execute("PRAGMA journal_mode=WAL")
            self._connection.execute("PRAGMA synchronous=NORMAL")
            self._connection.execute("PRAGMA busy_timeout=5000")
            self._connection.execute(
                """
                CREATE TABLE IF NOT EXISTS request_history (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    timestamp REAL NOT NULL,
                    provider_id TEXT NOT NULL,
                    provider TEXT NOT NULL,
                    model TEXT NOT NULL,
                    method TEXT NOT NULL,
                    path TEXT NOT NULL,
                    status INTEGER,
                    state TEXT NOT NULL,
                    cache_1h INTEGER NOT NULL,
                    latency_ms REAL NOT NULL,
                    queue_ms REAL NOT NULL,
                    retries INTEGER NOT NULL,
                    retries_429 INTEGER NOT NULL,
                    bytes_in INTEGER NOT NULL,
                    bytes_out INTEGER NOT NULL,
                    input_tokens INTEGER NOT NULL,
                    context_tokens INTEGER NOT NULL,
                    output_tokens INTEGER NOT NULL,
                    cached_tokens INTEGER NOT NULL,
                    reasoning_tokens INTEGER NOT NULL,
                    total_tokens INTEGER NOT NULL,
                    tokens_per_second REAL NOT NULL,
                    error_detail TEXT NOT NULL
                )
                """
            )
            columns = {
                row[1]
                for row in self._connection.execute("PRAGMA table_info(request_history)")
            }
            for name, declaration in _MIGRATIONS.items():
                if name not in columns:
                    self._connection.execute(
                        f"ALTER TABLE request_history ADD COLUMN {name} {declaration}"
                    )
            self._connection.executescript(
                """
                CREATE INDEX IF NOT EXISTS history_timestamp
                    ON request_history(timestamp DESC);
                CREATE INDEX IF NOT EXISTS history_provider_timestamp
                    ON request_history(provider_id, timestamp DESC);
                """
            )
        except BaseException:
            self._closed = True
            if self._connection is not None:
                self._connection.close()
            raise

    @staticmethod
    def _where(period: str) -> tuple[str, tuple]:
        try:
            hours = PERIODS[period]
        except KeyError as error:
            raise ValueError("Unknown statistics period") from error
        if hours is None:
            return "", ()
        return "WHERE timestamp >= ?", (time.time() - hours * 3600,)

    def record(self, event: dict) -> bool:
        values = (
            event["timestamp"],
            event["provider_id"],
            event["provider"],
            event.get("model") or "—",
            event["method"],
            event["path"],
            event["status"],
            event["state"],
            int(bool(event.get("cache_1h"))),
            event["latency_ms"],
            event.get("queue_ms", 0),
            event.get("retries", 0),
            event.get("retries_429", 0),
            event.get("bytes_in", 0),
            event.get("bytes_out", 0),
            event.get("input_tokens", 0),
            event.get("context_tokens", event.get("input_tokens", 0)),
            event.get("output_tokens", 0),
            event.get("cached_tokens", 0),
            event.get("reasoning_tokens", 0),
            event.get("total_tokens", 0),
            event.get("tokens_per_second", 0),
            _safe_error_detail(event.get("error_detail")),
        )
        with self._lock:
            if self._closed:
                return False
            try:
                self._connection.execute(
                    """
                    INSERT INTO request_history (
                        timestamp, provider_id, provider, model, method, path, status,
                        state, cache_1h, latency_ms, queue_ms, retries, retries_429, bytes_in, bytes_out,
                        input_tokens, context_tokens, output_tokens, cached_tokens,
                        reasoning_tokens, total_tokens, tokens_per_second, error_detail
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    values,
                )
            except sqlite3.Error:
                return False
            return True

    def stats(self, period: str) -> dict:
        where, params = self._where(period)
        with self._lock:
            if self._closed:
                return self._empty_stats(period)
            row = self._connection.execute(
                f"""
                SELECT
                    COUNT(*) AS requests,
                    COALESCE(SUM(state = 'ok'), 0) AS successes,
                    COALESCE(SUM(state = 'error'), 0) AS errors,
                    COALESCE(SUM(state = 'cancelled'), 0) AS cancelled,
                    COALESCE(AVG(latency_ms), 0) AS average_ms,
                    COALESCE(SUM(input_tokens), 0) AS input_tokens,
                    COALESCE(SUM(context_tokens), 0) AS context_tokens,
                    COALESCE(SUM(output_tokens), 0) AS output_tokens,
                    COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
                    COALESCE(SUM(reasoning_tokens), 0) AS reasoning_tokens,
                    COALESCE(SUM(total_tokens), 0) AS total_tokens,
                    COALESCE(AVG(tokens_per_second), 0) AS tokens_per_second,
                    COALESCE(SUM(bytes_in), 0) AS bytes_in,
                    COALESCE(SUM(bytes_out), 0) AS bytes_out,
                    COALESCE(SUM(retries), 0) AS retries,
                    COALESCE(SUM(retries_429), 0) AS retries_429
                FROM request_history {where}
                """,
                params,
            ).fetchone()
            result = dict(row)
            count = result["requests"]
            if count:
                p95 = self._connection.execute(
                    f"""
                    SELECT latency_ms FROM request_history {where}
                    ORDER BY latency_ms LIMIT 1 OFFSET ?
                    """,
                    (*params, min(count - 1, int(count * 0.95))),
                ).fetchone()
                result["p95_ms"] = p95[0]
            else:
                result["p95_ms"] = 0
        result["period"] = period
        return result

    @staticmethod
    def _empty_stats(period: str) -> dict:
        return {
            "requests": 0,
            "successes": 0,
            "errors": 0,
            "cancelled": 0,
            "average_ms": 0,
            "p95_ms": 0,
            "input_tokens": 0,
            "context_tokens": 0,
            "output_tokens": 0,
            "cached_tokens": 0,
            "reasoning_tokens": 0,
            "total_tokens": 0,
            "tokens_per_second": 0,
            "bytes_in": 0,
            "bytes_out": 0,
            "retries": 0,
            "retries_429": 0,
            "period": period,
        }

    def breakdown(self, period: str, limit: int = 50) -> list[dict]:
        where, params = self._where(period)
        with self._lock:
            if self._closed:
                return []
            rows = self._connection.execute(
                f"""
                SELECT provider, model, COUNT(*) AS requests,
                       COALESCE(SUM(total_tokens), 0) AS total_tokens,
                       COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
                       COALESCE(AVG(latency_ms), 0) AS average_ms
                FROM request_history {where}
                GROUP BY provider, model
                ORDER BY total_tokens DESC, requests DESC
                LIMIT ?
                """,
                (*params, limit),
            ).fetchall()
        return [dict(row) for row in rows]

    def recent(self, period: str, limit: int = 100) -> list[dict]:
        where, params = self._where(period)
        with self._lock:
            if self._closed:
                return []
            rows = self._connection.execute(
                f"""
                SELECT * FROM request_history {where}
                ORDER BY timestamp DESC LIMIT ?
                """,
                (*params, limit),
            ).fetchall()
        return [dict(row) for row in rows]

    def close(self) -> None:
        with self._lock:
            if not self._closed:
                self._closed = True
                try:
                    self._connection.close()
                except sqlite3.Error:
                    pass

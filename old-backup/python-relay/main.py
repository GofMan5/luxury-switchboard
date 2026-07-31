#!/usr/bin/env python3
"""Provider Switchboard entrypoint."""

import argparse
import threading

from relay_config import ConfigError
from relay_http import (
    LISTEN,
    RelayHandler,
    RelayServer,
    ResponseInspector,
    echo_cache_for_one_hour,
    json_object,
    request_model,
    retry_after_seconds,
    upstream_target,
)
from relay_runtime import (
    ClientDisconnected as _ClientDisconnected,
    ProviderRegistry,
    RateGate as EchoGateGate,
    RelayMetrics,
    RelayStopping as _RelayStopping,
    TokenUsage,
)


def console(server):
    print("Commands: Enter/s = next provider, number = select, l = list, q = quit")
    while True:
        try:
            command = input("> ").strip().lower()
        except EOFError:
            return
        providers = server.providers()
        if command in {"", "s", "switch"}:
            name, upstream = server.toggle()
            print(f"Active: {name} ({upstream.geturl()})")
        elif command in {"l", "list"}:
            for index, provider in enumerate(providers, 1):
                marker = "*" if provider.active else " "
                print(f"{marker} {index}: {provider.name} · {provider.upstream}")
        elif command.isdecimal() and 1 <= int(command) <= len(providers):
            name, upstream = server.select(int(command) - 1)
            print(f"Active: {name} ({upstream.geturl()})")
        elif command in {"q", "quit", "exit"}:
            return
        else:
            print("Use Enter, l, a provider number, or q.")


def main():
    parser = argparse.ArgumentParser(description="Switchable local AI provider relay")
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--plain", action="store_true", help="use the text console")
    modes.add_argument("--headless", action="store_true", help="run without a UI")
    parser.add_argument(
        "--start-tunnel",
        action="store_true",
        help="start the saved public tunnel before opening the UI",
    )
    parser.add_argument("--port", type=int, default=LISTEN[1], help="loopback port")
    parser.add_argument(
        "--provider", default=None, help="override the saved initial provider id"
    )
    parser.add_argument(
        "--rpm",
        "--echo-rpm",
        dest="echo_rpm",
        type=int,
        default=None,
        help="override the saved EchoGate provider RPM; 0 is unlimited",
    )
    args = parser.parse_args()
    if not 1 <= args.port <= 65535:
        parser.error("--port must be between 1 and 65535")
    if args.echo_rpm is not None and args.echo_rpm < 0:
        parser.error("--rpm must be zero or greater")

    listen = (LISTEN[0], args.port)
    try:
        server = RelayServer(listen, RelayHandler, echo_rpm=args.echo_rpm)
        if args.provider is not None:
            server.select(args.provider)
    except (ConfigError, OSError, KeyError, ValueError) as error:
        raise SystemExit(
            f"Cannot start relay on http://{listen[0]}:{listen[1]}: {error}"
        ) from error

    if args.headless:
        print(f"Relay: http://{listen[0]}:{listen[1]}/v1")
        try:
            server.serve_forever(poll_interval=0.2)
        except KeyboardInterrupt:
            print("\nStopped.")
        finally:
            server.server_close()
        return

    plain = args.plain
    if not plain:
        try:
            from relay_ui import RelayApp
        except ModuleNotFoundError as error:
            if error.name not in {"rich", "textual"}:
                raise
            print("Textual is not installed; starting plain mode.")
            print("Install the dashboard with: python -m pip install -r requirements.txt")
            plain = True

    server_thread = threading.Thread(
        target=server.serve_forever,
        kwargs={"poll_interval": 0.2},
        name="provider-relay",
        daemon=True,
    )
    server_thread.start()
    if args.start_tunnel:
        try:
            server.start_tunnel()
        except Exception:
            print("Public tunnel could not be started; open Tunnel and retry.")
    try:
        if plain:
            print(f"Relay: http://{listen[0]}:{listen[1]}/v1")
            console(server)
        else:
            RelayApp(server).run()
    except KeyboardInterrupt:
        print("\nStopped.")
    finally:
        server.shutdown()
        server_thread.join(timeout=2)
        server.server_close()


if __name__ == "__main__":
    main()

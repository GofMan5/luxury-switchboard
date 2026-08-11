# Provider Switchboard

Native Windows and Linux desktop relay for OpenAI- and Anthropic-compatible providers.

- Local relay: `http://127.0.0.1:8798/v1`
- Go owns routing, key/RPM queues, retries, streams, storage and public tunnel policy.
- Tauri v2 is a thin native lifecycle/stdin-stdout bridge.
- React/Vite provides the adaptive mouse-first interface.

The implementation is split into hexagonal vertical slices under `backend/internal/slices` and `frontend/src/features`. The previous Python/Textual release is preserved unchanged in `old-backup/python-relay`.

## Development

Common requirements: Go 1.25+, Rust 1.96+, Node.js 24+ and pnpm 10+. Windows additionally needs WebView2 and OpenSSH. Ubuntu/Debian build hosts need:

```bash
sudo apt-get install libwebkit2gtk-4.1-dev libappindicator3-dev librsvg2-dev patchelf libfuse2
```

```powershell
pnpm install
pnpm dev
```

The debug desktop uses relay port `18798`, so it does not take over the normal `8798` listener. Build the native package for the current host with:

```powershell
pnpm build
```

Windows produces an NSIS installer; Linux produces AppImage and deb packages. All current-version outputs and their `SHA256SUMS.txt` are collected under `artifacts/release`. The **Build desktop** GitHub workflow builds both native hosts and publishes one `Switchboard-Windows-Linux` artifact containing the same flat release folder.

From Windows with Docker Desktop, `pnpm build:linux` performs the isolated Linux build and merges AppImage/deb into that same folder without changing the Windows toolchain or `node_modules`.

## Core workflow

1. Add any OpenAI/Anthropic-compatible provider in **Providers**. Configure its base path/query, dialect, model-discovery path, exact auth header, RPM, one-hour cache policy and optional Responses image-tool bridge. Loopback HTTP is allowed; remote plaintext HTTP is rejected.
2. Add one or more secrets in **API Keys**, set priority/RPM and optionally a per-key HTTP(S)/SOCKS proxy.
3. Use **Model Routes** to discover, filter and test models. The catalog shows where each model is already published (**Relay** / **Tunnel** badges with the public alias), the checkboxes start as a mirror of the published routes, and one **Apply** step publishes the newly selected models and clears the deselected ones for the current provider. Hand-made aliases and routes of other providers stay untouched; edit those per row.
4. Configure the public API key, per-IP RPM, context cap and optional publisher profile in **Tunnel**.
5. Inspect live requests, HTTP status, queue/retries, context, cached/reasoning/processed tokens and generation speed in **Live Activity**. Tunnel users and their sanitized persistent events are separate under **Clients**, where every address can also be banned or annotated with an owner-only note.

Provider switches, active endpoint/auth/cache changes and local Relay route changes cancel old in-flight work. Standby-provider and Tunnel-route edits apply to new requests without aborting unrelated local generations. Retryable transport errors, truncated JSON/SSE, `429`, `504` and other `5xx` responses are retried before client commit. RPM and model/balance cooldowns remain in FIFO queues until capacity returns or the client cancels.

## Client compatibility

Recent Responses clients (for example Codex 0.14x and newer) no longer send the documented top-level `tools` array: they declare tools inside the request input as an `additional_tools` item with nested `namespace` groups, and they use freeform `custom` tools. Providers that implement only the documented Responses schema drop those items, the model receives no tool definitions and leaks raw call syntax such as `to=functions.exec {"cmd":"…"}` into its assistant text.

The relay therefore normalizes every `/v1/responses` request into the documented shape — namespaces flattened, nested names given stable provider-safe aliases, freeform tools expressed as functions taking one `input` string, replayed history rewritten to one string output per call — and converts the provider answers back into exactly what the client declared, including freeform `custom_tool_call` items and their argument events. It also repairs streaming lifecycles that clients refuse to consume: items announced as already `completed`, and missing `response.content_part.added` events without which every text delta of the item is discarded. Requests that already use the documented schema are forwarded byte for byte.

## Client bans and notes

Each tunnel address can be banned or annotated under **Clients**. A ban is refused at the gateway before any queue slot, route lookup or provider call, with the same neutral body as every other rejection; generations already in flight are allowed to finish. Refused attempts stay visible in the client history. Bans and notes are owner-only governance state: they are stored next to the local request history, survive restarts, and never appear in public responses, `/v1/models`, telemetry or shared control.

## API keys

Switchboard never imports API credentials from process or user environment variables. Add every provider key explicitly in **API Keys**; it is encrypted with current-user DPAPI and remains write-only after saving.

## Public tunnel

The local gateway listens only on `127.0.0.1`. An optional publisher profile has the strict form:

```text
v1.<port 20000-29999>.<48 lowercase hex slug>
```

With a profile configured, Switchboard uses the dedicated identity
`%LOCALAPPDATA%\ProviderSwitchboard\ssh\model-tunnel_ed25519`, ignores the user's SSH config/agent, pins the VPS Ed25519 host key, creates a reverse port, and reports **Online** only after authenticated HTTPS `/v1/models` returns exactly the selected public aliases. Broken sessions reconnect with bounded backoff.

Shared pause/resume/stop uses a separate `model-tunnel_control_ed25519` identity. The UI receives only revision, neutral display name and state; tunnel IDs, owners, ports, slugs and provider data never cross the control response.

The VPS replacement is the Go command `backend/cmd/tunnel-hub`. Deployment templates are in `deploy/`.

## Privacy boundary

The public tunnel is an allowlist gateway, not a passthrough proxy:

- only exact supported inference paths and enabled Tunnel routes are accepted;
- `/v1/models` is synthetic and never calls a provider;
- only authenticated providers with a configured key are publishable;
- provider/upstream names, URLs, IDs, raw model IDs, `owned_by`, fingerprints, auth/proxy values and unsafe headers are removed before commit;
- JSON and SSE are fully validated before public output; provider failures become generic local errors;
- explicit provider/source probes receive the configured Luxury Private identity locally without contacting upstream;
- request/response bodies and secrets are never written to activity or tunnel history.

Image generation uses each provider's native JSON/multipart endpoint by default. Providers that expose image generation through the Responses tool can enable the profile-level bridge; the built-in EchoGate profile enables it by default and supports Codex `gpt-image-2` plus public aliases without forcing that behavior onto custom providers.

## Storage

Current-user data lives under `%LOCALAPPDATA%\ProviderSwitchboard` on Windows and `${XDG_CONFIG_HOME:-~/.config}/provider-switchboard` on Linux:

- encrypted provider/key/settings/route/tunnel configuration;
- `history.v2.db` for relay activity;
- `tunnel_history.v1.db` for separate sanitized per-client tunnel events;
- pinned SSH known-hosts and owner-provided publisher/control identities.

Windows encrypts configuration with current-user DPAPI. Linux stores the AES-256 master key in the desktop Secret Service (for example GNOME Keyring or KWallet) and fails closed when no user keyring is available; credentials are never downgraded to plaintext files.

On Linux, launch Switchboard as the signed-in desktop user, never through `sudo`. If secure storage is reported unavailable, install/start and unlock GNOME Keyring or the KWallet Secret Service bridge, then restart Switchboard; unreadable routes are blocked instead of silently falling back to another provider.

No identity or API key belongs in a release archive or git.

## Checks

```powershell
pnpm check
pnpm backend:race
cargo test --manifest-path src-tauri/Cargo.toml
```

Linux VPS hub build:

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'
go -C backend build -trimpath -o tunnel-hub ./cmd/tunnel-hub
```

For the Windows friend archive, after the final NSIS installer and root `SHA256SUMS.txt` are verified, commit only that checksum file on top of the exact source commit used for the build, then run `scripts/package-friend.ps1`. The packager rejects dirty or post-checksum source, records both commits, and excludes SSH identities, encrypted state, databases and private-key formats.

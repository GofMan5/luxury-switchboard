# Provider Switchboard

Native Windows desktop relay for OpenAI- and Anthropic-compatible providers.

- Local relay: `http://127.0.0.1:8798/v1`
- Go owns routing, key/RPM queues, retries, streams, storage and public tunnel policy.
- Tauri v2 is a thin native lifecycle/stdin-stdout bridge.
- React/Vite provides the adaptive mouse-first interface.

The implementation is split into hexagonal vertical slices under `backend/internal/slices` and `frontend/src/features`. The previous Python/Textual release is preserved unchanged in `old-backup/python-relay`.

## Development

Requirements: Go 1.25+, Rust stable, pnpm 10+, WebView2 and Windows OpenSSH for VPS publishing.

```powershell
pnpm install
pnpm dev
```

The debug desktop uses relay port `18798`, so it does not take over the normal `8798` listener. Build the NSIS package with:

```powershell
pnpm build
```

## Core workflow

1. Add any OpenAI/Anthropic-compatible provider in **Providers**. Configure its base path/query, dialect, model-discovery path, exact auth header, RPM, one-hour cache policy and optional Responses image-tool bridge. Loopback HTTP is allowed; remote plaintext HTTP is rejected.
2. Add one or more secrets in **API Keys**, set priority/RPM and optionally a per-key HTTP(S)/SOCKS proxy.
3. Use **Model Routes** to discover and test models, then assign selected models independently to the local Relay or public Tunnel. A second click on **All models** clears the selection.
4. Configure the public API key, per-IP RPM, context cap and optional publisher profile in **Tunnel**.
5. Inspect live requests, HTTP status, queue/retries, context, cached/reasoning/processed tokens and generation speed in **Live Activity**. Tunnel users and their sanitized persistent events are separate under **Clients**.

Provider switches, active endpoint/auth/cache changes and local Relay route changes cancel old in-flight work. Standby-provider and Tunnel-route edits apply to new requests without aborting unrelated local generations. Retryable transport errors, truncated JSON/SSE, `429`, `504` and other `5xx` responses are retried before client commit. RPM and model/balance cooldowns remain in FIFO queues until capacity returns or the client cancels.

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

Current-user data lives under `%LOCALAPPDATA%\ProviderSwitchboard`:

- encrypted provider/key/settings/route/tunnel configuration;
- `history.v2.db` for relay activity;
- `tunnel_history.v1.db` for separate sanitized per-client tunnel events;
- pinned SSH known-hosts and owner-provided publisher/control identities.

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

After the final installer and `SHA256SUMS.txt` are verified, commit only the checksum file on top of the exact source commit used for the build, then create the friend archive with `scripts/package-friend.ps1`. The packager rejects dirty or post-checksum source, records both commits, and excludes SSH identities, DPAPI files, databases and private-key formats.

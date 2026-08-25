# Luxury Switchboard

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

Windows produces an NSIS installer; Linux produces AppImage and deb packages. Every run builds both editions: the owner outputs and their `SHA256SUMS.txt` land in `artifacts/release`, the public ones in `artifacts/release-public`. The **Build desktop** GitHub workflow builds both native hosts and publishes one `Luxury-Switchboard-Windows-Linux` artifact holding those same two folders.

From Windows with Docker Desktop, `pnpm build:linux` performs the isolated Linux build of both editions and merges the AppImage/deb of each into the matching folder without changing the Windows toolchain or `node_modules`.

## Core workflow

1. Add any OpenAI/Anthropic-compatible provider in **Providers**. Configure its base path/query, dialect, model-discovery path, exact auth header, RPM, one-hour cache policy and optional Responses image-tool bridge. Loopback HTTP is allowed; remote plaintext HTTP is rejected.
2. Add one or more secrets in **API Keys**, set priority/RPM and optionally a per-key HTTP(S)/SOCKS proxy. **Bulk import** takes a whole pasted list at once — one `label secret` per line, sharing the limit and proxy of the batch — and reports what it added, which lines were already configured and which could not be read instead of losing the rest of the paste.
3. Use **Model Routes** to discover, filter and test models. The catalog shows where each model is already published (**Relay** / **Tunnel** badges with the public alias), the checkboxes start as a mirror of the published routes, and one **Apply** step publishes the newly selected models and clears the deselected ones for the current provider. Hand-made aliases and routes of other providers stay untouched; edit those per row. A catalog too large to load in one piece is reported as exactly that, rather than being quietly shortened — a model missing from the list is a model you cannot publish.
4. Configure the public API key, per-IP RPM, context cap and optional publisher profile in **Tunnel**.
5. Inspect live requests, HTTP status, queue/retries, context, cached/reasoning/processed tokens and generation speed in **Live Activity**. Tunnel users and their sanitized persistent events are separate under **Clients**, where every address can also be banned or annotated with an owner-only note.
6. Review what providers actually sent back in **Guardrails**, and decide there whether a suspicious answer is only recorded or refused outright.

Provider switches, active endpoint/auth/cache changes and local Relay route changes cancel old in-flight work. Standby-provider and Tunnel-route edits apply to new requests without aborting unrelated local generations. Retryable transport errors, truncated JSON/SSE, `429`, `504` and other `5xx` responses are retried before client commit. RPM and model/balance cooldowns remain in FIFO queues until capacity returns or the client cancels.

One bad answer cannot cost you the window. If a record arrives in a shape a screen did not expect, that screen alone is replaced with a short notice and a **Try again** button; the sidebar, the runtime bar and every other tab keep working, and navigating anywhere clears the notice. The notice never repeats what the provider or the record said — those details go to the log.

## Editions

Two editions build from the same tree. The **owner** edition is the full product. The **public** edition is what ordinary users get: it has no Tunnel, no Tunnel Clients and no Shared Control, and Model Routes serves the local relay only.

The public edition is cut, not hidden. Its sidecar is compiled without the publishing stack, so the tunnel gateway, per-client governance, the SSH publisher and the hub client are absent from the binary and cannot be reached even by driving the sidecar directly. Its interface bundle contains no owner workspace chunks, and `system.handshake` reports the edition and the capabilities that binary actually serves.

`pnpm build` and `pnpm build:linux` produce both editions in one run: the owner build lands in `artifacts/release`, the public build in `artifacts/release-public`, each with its own `SHA256SUMS.txt`. The release fails if an owner workspace or an owner command is found in the public output.

## Guardrails

Every provider answer is inspected locally before your client sees a byte of it. A provider that is happy to serve cheap inference is not automatically a provider you want executing ideas inside your terminal: the answer is what your assistant then acts on, and the assistant is running on your machine with your files.

The relay inspects the **final** body, in the dialect your client asked for, after every translation and stream repair — a payload cannot hide in an intermediate shape the relay was still rewriting. What gets inspected is decided by the endpoint you called and by the bytes themselves, never by the label the provider put on them: an answer sent as `text/plain` is still read, because your client reads the body too. Stream deltas are joined before matching, so splitting `curl x | sh` across three events does not get past anything, and neither does splitting it across the `data:` lines of one event. Arguments of a tool call are also read decoded: inside JSON a script is escaped, and `#!/bin/sh\ncurl x | sh` would otherwise reach the rules with no word boundary in front of `curl`. A body in a shape no dialect recognises is read as plain JSON strings rather than skipped, because one off-spec field is enough to fail a whole schema — and a body our own parser refuses outright is read as bytes for the same reason, since a client written against a different parser will accept what ours rejected. An answer packing several JSON objects into one payload is read to the end, not just to the first of them. An answer too large to buffer still gets the part that was read inspected, and the part that went unread is reported as exactly that — padding an answer past the ceiling buys a note in your findings, not silence. Detection covers shell download-and-execute chains, decoded payloads executed directly, credential and wallet paths, persistence via scheduled tasks and startup entries, known malicious domains and addresses, and a protocol anomaly of its own: a tool call in an answer to a request that declared no tools. Your client has nothing to run such a call with, so the provider put it there.

Inspection is inline, so its cost is your wait. Each rule states the literals it cannot match without, and a rule whose literals are absent from the answer never runs — half a megabyte of ordinary prose reaches no rule at all and is checked in about 60 ms instead of four seconds, and a typical answer in well under a millisecond.

A refused answer costs an attempt, not your run. In Block mode the request is simply sent again, on the same budget as a provider that failed out loud, and a model that sampled something ugly once usually does not do it twice; you wait a little longer and get a clean answer. If every attempt earns a refusal the request fails, and every refused attempt stays in your findings — the re-roll hides nothing. Public tunnel traffic is not re-rolled: there a refusal is final.

Three modes, chosen under **Guardrails** and applied on the next request without a restart:

- **Off** — answers are forwarded without inspection.
- **Monitor** (default) — everything is inspected and recorded, and every answer is still delivered. This is the default deliberately: the rules match shell and network idiom that an honest coding assistant produces all day, and refusing all of it would destroy legitimate work.
- **Block** — an answer with a high-severity finding is refused before it reaches your client.

Findings stay on your machine, in memory only, and never travel anywhere. Each one says where the payload was, not just that it existed: the same command explained in prose and placed in a tool call your client would run are two different findings, because only one of them is about to execute. The detection rules themselves are never shown: the workspace reports how many are loaded and what they caught, not the patterns, so a report cannot be turned into an evasion guide. A refused answer leaves the public tunnel as the same neutral error as an unavailable provider — indistinguishable by status, body, headers or client history — so nobody outside can probe the rule set by watching which answers fail.

The list on screen is the newest part of the journal, not always all of it: one hostile answer can carry a finding for every rule it tripped, so the oldest records are held back rather than letting a single page outgrow what the app can display at once. When that happens the section says so — **Newest 60 of 250** — and the **Findings** count always reports the recorded total, never the length of the list beneath it.

Both editions inspect. A public user has the same right to know what a provider sent them as the owner does.

Rule and indicator data is vendored from [holone](https://github.com/vanndh/holone) (MIT); attribution and the full licence live in `backend/internal/slices/guardrails/adapters/ruleset/LICENSE-holone.txt`, and both installers place that notice next to the application because the rules themselves are compiled in. The set is not copied unchanged, and the rule file's own `note` records every difference: one rule was added for a decoded payload executed directly (`base64 -d | sh`), which nothing in the set caught; seven were narrowed because at high severity they refused ordinary developer answers — an answer about advertising, a TypeScript type alias, `npm install eslint-plugin-import`, a note about Chrome DevTools; and one was repaired because it could not match six of the eight things it listed, so `certutil -decode payload.b64 payload.exe` went undetected while `certutil x-decode` did not. Each still matches the attack it was written for. Known indicators are also split by how specific they are: a domain, an address or a full task path is high severity, while a bare name such as `proxy.exe` is recorded at medium, because Block mode must not refuse an honest answer that happens to mention it.

## Client compatibility

Recent Responses clients (for example Codex 0.14x and newer) no longer send the documented top-level `tools` array: they declare tools inside the request input as an `additional_tools` item with nested `namespace` groups, and they use freeform `custom` tools. Providers that implement only the documented Responses schema drop those items, the model receives no tool definitions and leaks raw call syntax such as `to=functions.exec {"cmd":"…"}` into its assistant text.

The relay therefore folds every declaration into the documented shape — namespaces flattened, nested names given stable provider-safe aliases, replayed history reduced to one string output per call — and converts the provider answers back into exactly what the client declared, including freeform `custom_tool_call` items and their argument events. It also repairs streaming lifecycles that clients refuse to consume: items announced as already `completed`, and missing `response.content_part.added` events without which every text delta of the item is discarded. All of that survives a provider that spreads one event across several `data:` lines, which the spec allows and which used to make the relay forward the block untouched. Requests that already use the documented schema are forwarded byte for byte.

A freeform `custom` tool travels exactly as declared, grammar included. Rewriting it into a function taking one `input` string discards that grammar, and the model then answers `apply_patch` with an empty argument string or `exec` with a JSON object instead of the script: every call fails and the assistant starts narrating what it meant to do instead of doing it. Replayed freeform history keeps its own item types for the same reason — retyping it against the declared tool is what made providers reject the turn that follows a tool call. A provider that refuses freeform tools gets them re-expressed as documented functions on the retry, tools and history together, and only after it says so.

Providers served through the Chat Completions translation are the one exception, because that dialect has no freeform type at all: there the tool and its replayed history are converted on the way out, so the model still receives a definition for the tool it is about to be asked to use. A provider whose request path is rewritten mid-flight — the endpoint probe that discovers a chat-only gateway — never changes the dialect the caller is answered in.

The answer coming back from such a provider carries one more thing worth keeping: why it stopped. Chat states that in `finish_reason` and puts a declined turn in `refusal`, and all three ways of saying "there is no answer here" used to be dropped in translation. A refusal arrived as a complete response with nothing in it, which reads as a provider that answered with silence; so did a turn the provider's own filter took; and an answer cut off at the token budget arrived as half a sentence stamped complete. The refusal is now carried as the message text, and a truncated or filtered turn is marked incomplete with the reason, in the envelope and in the event that closes the stream.

Two more failures end an agent run after a single tool call, and both are handled on the retry path. A turn that replays reasoning `encrypted_content` is refused whenever the seal was produced for a different account than the one now answering, which key rotation and provider-side account pools make the normal case; the relay retries once without the seal, keeping the tool exchange and the summaries. And a provider stuck on `429` or `5xx` no longer retries without end: the attempt loop has a ceiling, so a request ends instead of holding the caller on keep-alives for hours.

## Client bans and notes

Each tunnel address can be banned or annotated under **Clients**. A ban is refused at the gateway before any queue slot, route lookup or provider call, with the same neutral body as every other rejection; generations already in flight are allowed to finish. Refused attempts stay visible in the client history. Bans and notes are owner-only governance state: they are stored next to the local request history, survive restarts, and never appear in public responses, `/v1/models`, telemetry or shared control.

A ban is only ever reported as applied once it has been written down. If that storage is unavailable, banning fails visibly instead of holding the decision until the next restart quietly drops it — stop the tunnel rather than trust a ban the app could not save.

## API keys

Luxury Switchboard never imports API credentials from process or user environment variables. Add every provider key explicitly in **API Keys**; it is encrypted with current-user DPAPI and remains write-only after saving.

## Public tunnel

The local gateway listens only on `127.0.0.1`. An optional publisher profile has the strict form:

```text
v1.<port 20000-29999>.<48 lowercase hex slug>
```

With a profile configured, Luxury Switchboard uses the dedicated identity
`%LOCALAPPDATA%\ProviderSwitchboard\ssh\model-tunnel_ed25519`, ignores the user's SSH config/agent, pins the VPS Ed25519 host key, creates a reverse port, and reports **Online** only after authenticated HTTPS `/v1/models` returns exactly the selected public aliases. Broken sessions reconnect with bounded backoff.

Shared pause/resume/stop uses a separate `model-tunnel_control_ed25519` identity. The UI receives only revision, neutral display name and state; tunnel IDs, owners, ports, slugs and provider data never cross the control response.

The VPS replacement is the Go command `backend/cmd/tunnel-hub`. Deployment templates are in `deploy/`.

## Privacy boundary

The public tunnel is an allowlist gateway, not a passthrough proxy:

- only exact supported inference paths and enabled Tunnel routes are accepted;
- `/v1/models` is synthetic and never calls a provider;
- only authenticated providers with a configured key are publishable;
- provider/upstream names, URLs, IDs, raw model IDs, `owned_by`, fingerprints, auth/proxy values and unsafe headers are removed before commit;
- an identifier too short to remove from prose without rewriting the prose — a two-letter internal hostname, a model id like `o3` — is left where the model happened to mention it, rather than the answer being refused for containing "detail" or "constructor". The structural fields are still renamed regardless of length, and every credential still refuses the answer at any length;
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

On Linux, launch Luxury Switchboard as the signed-in desktop user, never through `sudo`. If secure storage is reported unavailable, install/start and unlock GNOME Keyring or the KWallet Secret Service bridge, then restart Luxury Switchboard; unreadable routes are blocked instead of silently falling back to another provider.

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

# Engineering contract

## Product and architecture

- Production-quality code: clear responsibilities, validated inputs, bounded resources, useful errors and no hidden non-working controls.
- Backend and relay are Go; Tauri v2 is a thin lifecycle/sidecar bridge. Do not duplicate domain logic in Rust.
- Frontend and backend use hexagonal architecture and vertical slices: adapters → application → domain. Domain does not import UI, transport, storage or frameworks.
- A slice owns its scenario and communicates through small typed contracts/events, never another slice's internals. Composition roots only wire dependencies and lifecycle.
- No god objects/stores, global mutable state, speculative abstractions or wrappers without a responsibility. Extract shared code after real repetition; do not split coherent code merely to reduce line counts.
- Configuration has typed schemas, validated defaults, versioned migration and atomic encrypted persistence. New settings must participate in `Normalized()` so existing configurations remain valid.
- Operational values belong in Settings/provider profiles, not scattered environment reads. Only security invariants and absolute safety caps are hardcoded.
- Updates must be atomic and recoverable. The retired Python relay is no longer maintained in this repository; its source remains in Git history. Keep the existing user-configuration migration working independently of that source tree.

## Privacy and trust boundaries

- Never commit real credentials, proxy credentials, SSH identities, provider configuration, user traffic, local databases or exported backups. Use fictional data in tests and screenshots.
- Credentials are write-only after saving. The owner's Providers screen may show its configured alias/endpoint; public clients, telemetry, logs and crash reports must never receive provider metadata or upstream URLs.
- The explicit backup export is the sole plaintext-credential exception: the file and UI warn about it, environment-pinned builtin keys are excluded, and import uses the same validating managers as manual input.
- Logs contain sanitized status, timing, queue/retry and token subsets, never prompts, response bodies or credentials.
- Encrypted persistence has no plaintext fallback. Do not report successful durable changes when storage is missing or failed; unavailable history is not empty history.
- Local listeners remain loopback-only, remote upstreams require HTTPS, forbidden headers stay forbidden, and public tunnel privacy fails closed. Settings cannot weaken these invariants.
- Relay and public Tunnel routes are independent. The public boundary uses exact-match allowlisted routes and owner-defined model aliases only; no provider identity, upstream fields, `owned_by` or membership oracle.
- Public headers, JSON and SSE are sanitized. Rejected and unavailable providers have indistinguishable neutral errors, including history error codes.
- Structural identifiers are always renamed. Textual identifier markers shorter than four bytes are deliberately not redacted to avoid rejecting ordinary prose; actual credential values are sensitive at every length. Do not replace the deliberate substring policy with word-boundary matching.
- Ignore rules reduce accidental inclusion, not replace auditing: check both Git changes and history when investigating a leak. Removal from the working tree does not remove past commits; do not rewrite published history without explicit authorization.

## Protocol and backend reliability

- Go sidecar stdout is exclusively the versioned framed stdio protocol; diagnostics use stderr. Commands have correlation IDs, typed payload/results, terminal outcomes and cancellation.
- Validate contracts on both sides. Unknown versions, malformed and oversized frames fail safely without hanging or leaking data.
- Use the shared frame/payload byte budgets (`platform.MaxFrameBytes`, 256 KiB, and `platform.MaxPayloadBytes`), not record counts. Include envelope space. Oversized model catalogs are refused explicitly, never silently truncated; bounded read-only findings lists visibly report truncation.
- Streaming has backpressure, bounded buffers and correct shutdown/restart. Cancellation and provider changes release sockets, leases and queue slots; RPM queues are fair, cancellable and observable.
- Retry policy distinguishes transport errors, 429, 5xx, permanent errors and terminal stream outcomes. Content-policy refusals terminate on the first attempt without repairs or key rotation. Never return a broken stream as completed.
- Dialect translation converts rather than drops custom tools, replayed history, refusals and finish reasons. Chat `length`/`content_filter` become incomplete Responses outcomes/events. Endpoint probing changes a copied upstream URL, never the client's response dialect.
- Relay policy decisions use `canonicalPath`; upstream forwarding retains its own unchanged URL copy. Public tunnel routes remain an exact-match allowlist.
- SSE `data:` lines in one event are joined with `\n` before parsing, inspection or tool repair. Split deltas must behave like their combined form.
- Anthropic bodies carrying a one-hour cache TTL declare the matching beta capability, preserving existing client headers on a copy. Do not add Anthropic headers to other dialects or to unchanged bodies that were not inspected.
- Live Settings apply through `OnApplied`, including transport replacement, queues, retention, capacities and listener restart when the port changes. Do not mutate an in-use `http.Transport`.

## Guardrails

- Inspect the final client-dialect provider answer locally before the client sees content, after relay translation/repair. Content type alone cannot bypass inspection.
- The slice is `backend/internal/slices/guardrails`. The engine is local; vendored MIT rule data retains `LICENSE-holone.txt`, modification notes/version and installer attribution. UI fixtures must match the embedded ruleset version/count.
- Inspect accumulated stream deltas, decoded tool-argument strings and unknown JSON shapes. Rejected JSON and trailing objects are still inspected as raw bytes; malformed input cannot silently become a clean verdict.
- Oversized bodies are inspected up to the bounded prefix, never forwarded truncated. An unread tail gets a distinct truncation finding. Truncation-only records coalesce per provider; real detection attempts never coalesce.
- Deduplicate equivalent matches by rule, match and coarse source kind so prose cannot hide a tool-call finding, and repeated tool names cannot crowd out other detections.
- Default mode is `monitor`, not `block`. Per-provider overrides are persisted; global `off` clears overrides. An unreadable request alone must not manufacture an unsolicited-tool anomaly.
- Behavioral rules must preserve legitimate developer answers. Campaign domains/IPs/hashes/paths can be high severity; generic words and executable names are not campaign-specific evidence.
- Regex prefilters may skip work only when they cannot hide a match. Keep both generated per-rule positive coverage and negative/random coverage; measure performance rather than guessing.
- Blocked relay answers retry within the existing permanent-attempt budget and record each rejected attempt; monitor mode never retries a recorded answer. Public tunnel dispatch never re-rolls a refusal.
- Rule patterns never leave the backend. UI/stdout expose only bounded counts, categories and safe excerpts, not an evasion manual.

## Frontend quality

- App shell composes navigation, layout and lifecycle. State stays local to a scenario; use selectors, batching and bounded histories before adding speculative virtualization.
- Use one calm neutral-grey token system for typography, spacing, borders, radii and semantic status colors. No gradients, glow, decorative AI imagery or arbitrary card grids.
- Supported windows start at 800×600. Check compact, standard and wide layouts; no clipped controls, overlaps, unreadable tables or primary-workflow horizontal overflow.
- Every interactive control has clear labels and hover/focus/pressed/disabled/loading/error states. Keyboard navigation must work; double-click cannot be the only way to invoke an important action.
- Motion explains state changes and respects `prefers-reduced-motion`. Do not block the UI with synchronous I/O, unbounded queues or live-update full-app rerenders.
- `WorkspaceBoundary` wraps the workspace, not the shell: navigation must survive render failures. `key={route}` clears failed workspace instances on navigation. Fallback copy stays neutral rather than repeating provider-controlled error text.
- Capabilities from `system.handshake` determine available scenarios; never show a control for functionality the sidecar does not advertise.

## Build, identity and releases

- One full product only: tunnel and clients included, handshake `edition: "full"` retained for compatibility. Do not restore owner/public editions, build tags or a second installer.
- Product name: **Luxury Switchboard**. Do not rename internal identities: bundle ID `one.luxuryprivate.switchboard`, Go module path, `ProviderSwitchboard` data directory, encryption/keyring entropy and `X-Switchboard-*` headers.
- A product-name change creates a second Windows installation. `FRIEND-SETUP.md` must retain the old `Switchboard` uninstall warning while old installs remain in use.
- Versions stay on `1.0.x`; changes shipped to users increment the patch. Use `pnpm version:set [1.0.x]`, never manual version edits. It synchronizes Tauri config, Cargo manifest/lock, root/frontend manifests and Go `AppVersion`.
- UI version comes only from the handshake. Builds enforce version agreement and patch-only versioning; do not bypass these checks.
- `pnpm build`/`pnpm build:linux` collect this host's native installer into `artifacts/release`. A single Tauri configuration lists native targets; macOS bundles require a Mac, each CPU architecture its native runner.
- Release CI runs on `v*` tags across Windows/Linux/macOS x64/arm64 and collects one shared checksum manifest. `--checksums-only` works with plain Node, without building or requiring pnpm.
- Collect only bundle files written by the current build, using mtime; old installers with matching names are not valid candidates. The root `SHA256SUMS.txt` records exactly the current host installer; CI creates the combined all-platform manifest.
- New stdio commands need both a Go handler and the Rust `sidecar.rs` allowlist. Removed commands remain documented in the allowlist while older shells can meet newer sidecars; the sidecar decides whether a command exists.

## Delivery and validation

- Start with the smallest relevant targeted test/benchmark. Run independent checks in parallel and broad gates once on a stable milestone or a changed public/security boundary. Preserve incremental caches; no pointless full rebuilds or duplicate checks.
- Changed slices need domain/unit and adapter/integration coverage. Critical protocol, streaming, retry, cancellation, persistence and privacy paths need end-to-end coverage.
- Go changes pass format, vet, tests and race checks for concurrent code. Frontend changes pass typecheck, lint, tests and production build. Tauri bundles use verified sidecar bytes.
- UI changes are checked with actual interactions/screenshots at supported sizes. Live relay gates cover sequential/parallel large-context requests, terminal reasoning/SSE, cancellation/provider switch and images on a separate loopback port.
- Before committing, review the diff, secrets, artifacts and manifests; stop temporary listeners/processes. Documentation-only housekeeping does not require a new application release or installer rebuild.

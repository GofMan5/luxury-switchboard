# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

**[AGENTS.md](AGENTS.md) is the binding engineering contract** (architecture, privacy boundaries, relay reliability, guardrails, frontend quality, releases, definition of done). Read it before changing code; this file only adds orientation and commands.

## Commands

Toolchain: Go 1.26.9+, Rust (pinned in `rust-toolchain.toml`), Node 24+, pnpm 10.26.2.

```bash
pnpm install
pnpm dev                 # tauri dev: desktop app, debug relay on :18798 (Vite holds :1420 with strictPort)
pnpm check               # full gate: version:check, frontend check, go test, cargo check, plugin tests
pnpm build               # native installer for this host -> artifacts/release (+ SHA256SUMS.txt)
pnpm sidecar:build       # Go sidecar for the Rust host triple; needed before any cargo build/test
pnpm version:set 1.0.x   # the only way to bump versions (syncs Tauri, Cargo, package.json, Go AppVersion)
```

Backend (Go module in `backend/`):

```bash
go -C backend test ./...
go -C backend test ./internal/slices/relay/... -run TestName
go -C backend vet ./...
gofmt -l backend                       # CI fails on any output
go -C backend test -race ./internal/platform/... ./internal/slices/keypool/... ./internal/slices/relay/... \
  ./internal/slices/analytics/... ./internal/slices/activity/... ./internal/slices/publictunnel/... ./internal/app/...
```

Frontend (`frontend/`):

```bash
pnpm --dir frontend lint        # oxlint
pnpm --dir frontend typecheck
pnpm --dir frontend test        # vitest run
pnpm --dir frontend exec vitest run src/features/activity -t "test name"
pnpm --dir frontend build
```

Tauri and scripts:

```bash
cargo test --locked --manifest-path src-tauri/Cargo.toml     # run pnpm sidecar:build first
node --test scripts/plugin.test.mjs
node --test scripts/release-ci.test.mjs scripts/release-bundles.test.mjs scripts/release-checksums.test.mjs
```

CI (`.github/workflows/build-desktop.yml`) runs exactly these checks on every push/PR; `v*` tags additionally build six native installers, sign Windows ones through SignPath (`docs/CODE-SIGNING.md`) and collect one checksum manifest.

## Architecture

Three processes, one product (**Luxury Switchboard**): a local relay/switchboard between AI clients and upstream providers, with an optional public tunnel.

- **Go sidecar** (`backend/cmd/switchboard-sidecar`) owns all domain logic. Its stdout is exclusively the versioned framed stdio protocol (`backend/internal/platform/stdio`); diagnostics go to stderr.
- **Tauri v2 shell** (`src-tauri/src`) only manages the sidecar lifecycle and forwards frames. `sidecar.rs` holds the command allowlist: a new stdio command needs both a Go handler and an allowlist entry; removed commands stay listed for older shells.
- **React frontend** (`frontend/src`) talks to the sidecar through `platform/stdio` (`tauri-sidecar-session.ts`, frame decoder). Contracts mirroring the protocol live in `shared/contracts`. Capabilities from the `system.handshake` decide which scenarios are shown; the UI version also comes only from the handshake.

Both sides use hexagonal vertical slices (`adapters → application → domain`):

- Backend slices: `backend/internal/slices/<name>/{adapters,application,domain}` (relay, providers, keypool, guardrails, publictunnel, tunnel, settings, activity, analytics, plugins, updates, ...). Shared infrastructure in `backend/internal/platform` (stdio, encryptedfile, atomicfile, secretstore, batchqueue, ...). The composition root is `backend/internal/app/bootstrap` (`app.go` wires slices and lifecycle; its tests include protocol/shell contract and live-settings integration tests). `backend/internal/migration/pythonconfig` keeps migrating configs from the retired Python relay.
- Frontend: `src/app` is the shell (AppShell, navigation, CommandPalette, ServicesProvider, boot); `src/features/<name>/{adapters,application,domain,ui}` are the screens; `src/shared` holds contracts, tokens and UI parts.

Plugins (a user-installable script/theme system with permissions) are documented in `docs/PLUGINS.md`; the design token system in `docs/DESIGN-SYSTEM.md`.

## Working notes

- The frontend cannot run in a plain browser: `platform/stdio/create-session.ts` throws without `__TAURI_INTERNALS__`. For screenshots, alias that module to a throwaway mock session in a separate Vite config on a port other than 1420, and delete the mock afterwards. Check layouts at 800, 1024 and 1440 widths (`minWidth: 800`).
- `App.test.tsx` and other Vitest files run without globals: call `cleanup()` explicitly, otherwise one failure cascades.
- Relay policy decisions must use `canonicalPath` (no ServeMux; `/V1/Responses` must behave like `/v1/responses`).
- Do not edit source files through PowerShell `Set-Content`/`Out-File`; it corrupts UTF-8 and adds a BOM.
- Use only fictional keys, endpoints and traffic in tests, fixtures (`src/dev`) and screenshots.

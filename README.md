<div align="center">

# Luxury Switchboard

**A private local switchboard for AI providers.**<br/>
One OpenAI/Anthropic-compatible endpoint on your machine. Fair key pools, live TTFT tests,<br/>
honest cost insights — and a one-click public tunnel that owes nothing to anyone's cloud.

[![License: MIT](https://img.shields.io/badge/license-MIT-7d8590?style=flat-square)](LICENSE)
[![Release](https://img.shields.io/github/v/release/GofMan5/luxury-switchboard?style=flat-square&color=3fb950)](https://github.com/GofMan5/luxury-switchboard/releases/latest)
[![Platform](https://img.shields.io/badge/platform-Windows%20%C2%B7%20Linux-58a6ff?style=flat-square)](https://github.com/GofMan5/luxury-switchboard/releases/latest)
[![Stars](https://img.shields.io/github/stars/GofMan5/luxury-switchboard?style=flat-square&color=e3b341)](https://github.com/GofMan5/luxury-switchboard/stargazers)

![Overview](docs/screenshots/overview.png)

</div>

## What it is

Your models live behind providers with different dialects, limits, moods and price tags. Luxury Switchboard puts one calm, honest endpoint in front of all of them — `http://127.0.0.1:8798/v1` — and gives you the controls such a thing should have: keys that take turns fairly, retries that know a refusal from a crash, streams that are repaired rather than abandoned, and a cost estimate built from prices you set, in your currency, instead of guessed.

It is a native desktop app (Tauri + Go + React). Everything is local: the configuration is encrypted per user, the history is SQLite on your disk, and nothing phones home. The one outbound call the app ever makes on its own is a GitHub release check, and it can be read in the source in one file.

## The tour

<table>
<tr>
<td width="50%" valign="top">

**Live Activity** — every request as it happens: status, queue time, retries, token mix, speed. A request that is waiting says why.

![Activity](docs/screenshots/activity.png)

</td>
<td width="50%" valign="top">

**Insights** — usage and cost per provider and model, per day. Costs come from a price catalog you own, in the currency you bill in.

![Insights](docs/screenshots/insights.png)

</td>
</tr>
<tr>
<td width="50%" valign="top">

**Tests** — real streaming probes against one provider's models or all of them: time to first token, total, decode rate. A probe never queues ahead of your traffic; a busy pool says so.

![Tests](docs/screenshots/tests.png)

</td>
<td width="50%" valign="top">

**Model Routes** — discover each provider's catalog, publish models under your aliases, pin where they are served. A catalog too big to load is reported, never silently shortened.

![Routes](docs/screenshots/routes.png)

</td>
</tr>
<tr>
<td width="50%" valign="top">

**Guardrails** — every provider answer is inspected locally before your client sees a byte. Monitor by default, block when you say so.

![Guardrails](docs/screenshots/guardrails.png)

</td>
<td width="50%" valign="top">

**Settings** — every value applies the moment you save it, with explanations instead of lore. No restart, ever.

![Settings](docs/screenshots/settings.png)

</td>
</tr>
</table>

## Quick start

1. Download the installer from the [latest release](https://github.com/GofMan5/luxury-switchboard/releases/latest). Windows ships an NSIS setup, Linux an AppImage and a deb; `SHA256SUMS.txt` rides along.
2. Add a provider in **Providers** — any OpenAI- or Anthropic-compatible endpoint. Loopback HTTP is allowed; remote plaintext HTTP is refused.
3. Add keys in **API Keys** — one per line, priorities and RPM per key, an optional per-key proxy. Keys are encrypted with your OS user store and are write-only after saving.
4. Point your client at `http://127.0.0.1:8798/v1`. That is the whole setup.

## The tunnel, without the tunneling

Share your endpoint with someone in one click. The app runs a pinned, checksum-verified [cloudflared](https://github.com/cloudflare/cloudflared) quick tunnel in front of its own fail-closed public gateway — no account anywhere, no server of ours, no SSH keys, no dashboard, no IP addresses to paste. **Start** gives you an HTTPS URL and a stable access key; share both.

The honesty, up front: a quick tunnel re-rolls its public address on every start. The key is stable; the URL is per-session. If you need a permanent address, the README of any cloudflared install is ten minutes of your evening — the gateway underneath does not care which tunnel fronts it.

What the public side can see is decided by an allowlist, not by hope:

- only the aliases you publish, only the inference paths, only with the key;
- provider names, upstream URLs, raw model IDs, `owned_by` and auth material are structurally absent — not redacted-in-place, absent;
- a refused or failed provider answers with the same neutral error as an offline one, so the tunnel is not an oracle for your setup;
- per-client history, bans and notes live in their own store, shown under **Clients**, and never leave it.

![Tunnel](docs/screenshots/tunnel.png)

## Guardrails, because the answer is the attack surface

A provider that serves cheap inference is not automatically a provider you want thinking inside your terminal — the answer is what your assistant acts on next, on your machine, with your files. So every answer is inspected locally before your client sees a byte of it: the final body, in the dialect your client asked for, after every translation and repair. Stream deltas are joined before matching; tool-call arguments are read decoded; a body too large to buffer still has its read part inspected and the unread part reported as exactly that.

The engine skips a rule when the literals it cannot match without are absent — ordinary prose reaches almost no rule at all and is checked in under a millisecond. **Monitor** is the default on purpose: the rules match shell idiom an honest assistant writes all day, and **Block** is there for providers you do not trust. Rule and indicator data is vendored from [holone](https://github.com/vanndh/holone) (MIT, attribution ships with both installers); the differences this project made are recorded in the rule file's own `note`.

## One build, no tiers

There is exactly one installer, and it is the full product — tunnel included. Nothing is gated, hidden, or compiled out; the handshake lists every capability the binary serves, and the interface offers exactly those workspaces.

## Data & storage

Everything lives under `%LOCALAPPDATA%\ProviderSwitchboard` on Windows and `~/.config/provider-switchboard` on Linux: encrypted configuration (DPAPI / the desktop Secret Service keyring — plaintext fallback does not exist), the relay history database, and the tunnel's per-client store. Deleting the folder is a full uninstall of state; backing it up is a full backup. A deliberate in-app export writes keys in clear because that file is meant to open anywhere — the file itself and the UI both say so.

## Development

Go 1.25+, Rust 1.96+, Node.js 24+, pnpm 10+. Linux build hosts additionally:

```bash
sudo apt-get install libwebkit2gtk-4.1-dev libappindicator3-dev librsvg2-dev patchelf libfuse2
```

```powershell
pnpm install
pnpm dev      # desktop app, debug relay on :18798
pnpm check    # everything: go vet+tests (both editions), race, tsc, eslint, vitest, builds
pnpm build    # native installers for the current host, both editions
```

The architecture contract lives in [AGENTS.md](AGENTS.md) and is written to be read by people and coding agents alike: hexagonal vertical slices, editions cut at compile time, a versioned stdio protocol, and the hard rules — secrets never reflected, the public boundary fail-closed, an honest state instead of a confident guess.

## License

[MIT](LICENSE). Vendored rule data: [holone](https://github.com/vanndh/holone), MIT. The tunnel connector is [cloudflared](https://github.com/cloudflare/cloudflared), Apache-2.0, downloaded on first use at a pinned version and checksum.

<div align="center">

[![Star History](https://api.star-history.com/svg?repos=GofMan5/luxury-switchboard&type=Date)](https://star-history.com/#GofMan5/luxury-switchboard&Date)

</div>

# Security

## Reporting a vulnerability

Please **do not** open a public issue for a security problem: this project handles provider credentials, an encrypted on-disk store, and a public tunnel boundary, and the report is the payload.

Report privately through [GitHub Security Advisories](https://github.com/GofMan5/luxury-switchboard/security/advisories/new). You get an acknowledgement within a few days; a fix lands in a patch release with credit (or anonymity, your call).

## What is in scope

- Credential handling: keys, proxies, the encrypted stores, the backup export.
- The public tunnel boundary: sanitization, the allowlist, per-client identity, bans.
- The local relay listener and the stdio protocol between the shell and the sidecar.
- The tunnel connector download (pin + checksum) and the update check.

## Out of scope

- Issues in providers themselves, or in cloudflared (report those upstream).
- Anything requiring physical access to an unlocked machine.

## The standing rules

The non-negotiables are written down in [AGENTS.md](AGENTS.md): secrets are never reflected back after saving, the public boundary fails closed, and every security-relevant behavior has a named test. If you find a place where reality disagrees with that document, reality loses; please tell us.

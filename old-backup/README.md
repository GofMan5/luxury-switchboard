# Python relay backup

`python-relay/` is the preserved Python/Textual implementation captured before the Go + Tauri rewrite on 2026-07-31.

- Snapshot includes the working relay, TUI, tests and tunnel deployment templates.
- Runtime data, `.git`, caches, local databases, logs, credentials and `deploy/tunnels.json` are intentionally excluded.
- The snapshot is read-only during the rewrite. Fixes belong in the new Go/Tauri implementation.
- Last broad baseline: 134 tests executed, 132 passed. Two concurrency/key-rotation expectations failed and are retained as migration regression cases.
- Legacy headless launch from the repository root: `python old-backup/python-relay/main.py --headless --port 8798`.

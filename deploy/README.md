# Shared tunnel control (VPS side)

This is the reviewed deployment template for the VPS installation.

## Data flow

Each exact tunnel route under `luxuryprivate.duckdns.org` gets one private
`forward_auth` check before the site's existing application fallback. The
gate reads the atomic registry on every request: `running` returns 204;
`paused`, `stopped`, missing, or malformed state returns the same generic 503.
The original request body and the existing reverse proxy/SSE stream are never
proxied through the gate.

The SSH control account accepts only these exact `SSH_ORIGINAL_COMMAND` values:

```
v1 list
v1 pause <revision> <position>
v1 resume <revision> <position>
v1 stop <revision> <position>
```

Every owner key sees and controls the shared list. Responses contain only a
revision plus display `name` and `state`; the row position is the short-lived
control handle. Internal IDs and owners never cross SSH. The registry itself
stores no URL, slug, port, provider, model, API key, or proxy.

## Install/review checklist

1. Create the locked-password `tunnel-control` user with `/bin/sh`; install
   `tunnel_hub.py` as root-owned mode 0755.
2. Install the service and initialize `/var/lib/tunnel-hub/tunnels.json` from
   the example as `tunnel-control:tunnel-control`, mode 0640.
3. Install root-owned `/etc/tunnel-hub/authorized_keys`, mode 0640, and the
   sshd Match block. Run `sshd -t` before reloading sshd.
4. Add one forward-auth stanza to each existing exact Caddy route. Its ID must
   match the registry; keep the current reverse-proxy block unchanged. Run
   `caddy validate` before reloading Caddy.
5. Bind the gate only to the Caddy bridge address and restrict TCP/18080 to
   that bridge in the host firewall.

`pause` and `stop` block the public route immediately without a Caddy reload.
The hub deliberately does not guess or kill an sshd PID. A client reconciles
`stopped` by closing its own reverse tunnel; `resume` changes desired state to
`running`, allowing that client to reconnect safely.

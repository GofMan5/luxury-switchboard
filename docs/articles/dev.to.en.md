# DEV Community publication

Prepared article, not yet submitted. Suggested tags: `opensource`, `go`, `ai`, `showdev`. Read and adapt the authorial voice before publishing. Cover image: [launch.png](../media/launch.png).

---

# One localhost endpoint for multiple AI providers: a desktop approach

An AI client usually asks for an API base URL, a key, and a model name. That works well until several clients need to share several providers. Changing one upstream then becomes a small configuration migration across unrelated applications.

Luxury Switchboard moves that responsibility into a local desktop app. Clients keep a stable endpoint; the app owns provider credentials, model routes, key pools, and failover policy.

This is an open-source, MIT-licensed project with Windows, Linux, and macOS installers. It does not host models, include API credits, or require a Luxury Switchboard account.

## The request path

```text
Client -> http://127.0.0.1:8798/v1
       -> local route: my-chat
       -> configured provider / upstream model
```

`my-chat` is a user-defined alias, not a model supplied by the project. It can point to one upstream model or an ordered chain of providers. Those models can have different prices, limits, and behavior; a stable alias is not a promise that they are interchangeable.

![Model Routes with fictional demo data](https://raw.githubusercontent.com/GofMan5/luxury-switchboard/main/docs/screenshots/routes.png)

## Why a desktop app?

This project starts with a local owner's workflow: keep provider configuration in one place, see what requests are doing, and avoid maintaining a separate hosted control plane just to change an endpoint.

Go implements the backend and relay. Tauri v2 is a thin lifecycle/sidecar bridge; React renders the interface. The shell and sidecar communicate through a versioned framed stdio protocol with correlated commands and cancellation. Diagnostics go to stderr rather than mixing with protocol output.

The backend is split into scenarios such as providers, key pools, routes, relay, analytics, and tunnel. HTTP and storage adapters do not define the business policy in UI components.

This is not an argument that desktop software should replace centrally operated gateways. If an organization needs a shared server with its own identity and operational requirements, evaluate those requirements directly. Luxury Switchboard is desktop-first.

## A compatible endpoint is more than a URL

The difficult part of a relay is preserving the client's contract.

Chat Completions, Responses, and Anthropic Messages differ in request shapes, tool calls, refusals, and terminal events. A relay that copies only text can lose tool arguments. A relay that treats an abruptly closed SSE connection as success can report a broken answer as complete.

The code includes regression coverage for dialect translation, stream completion, retries, cancellation, and usage accounting. The important design constraints are:

- bound request bodies and buffers;
- release connections and queue slots when a client cancels;
- distinguish temporary failures from permanent outcomes;
- preserve incomplete and refusal semantics;
- do not rotate credentials to retry a content-policy refusal;
- keep the original alias separate from each upstream model ID during failover.

None of this establishes compatibility with every client. Streaming, tools, images, and cancellation need to be tested for the actual client/provider/model combination.

## Try one request before configuring an agent

Download the installer for your platform from the [latest release](https://github.com/GofMan5/luxury-switchboard/releases/latest).

1. In **Providers**, add a compatible endpoint and configure authentication. Remote endpoints require HTTPS.
2. Save your provider key in **API Keys** if the provider uses key-based authentication.
3. In **Model Routes → Local relay → Add route**, choose that provider and a real upstream model. Set the public model name to `my-chat` and keep the route enabled.
4. Confirm the relay address in the app. The default is `http://127.0.0.1:8798`.

For macOS/Linux:

```sh
curl --fail-with-body --max-time 120 \
  http://127.0.0.1:8798/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"my-chat","messages":[{"role":"user","content":"Reply with one short greeting."}],"max_tokens":32}'
```

Use an upstream model that supports this scenario or the app's dialect translation. This request goes to your provider and may incur charges. There is a [PowerShell example in the README](https://github.com/GofMan5/luxury-switchboard#make-your-first-request).

Then open **Live Activity** to inspect status, queueing, and retries. If it fails, check the enabled route, upstream model name, provider state, and credential before involving a more complicated client.

The example is an HTTP starting point, not a claim that a particular editor or coding agent has been certified against this release.

## Visibility without a transcript in the logs

For operational diagnosis, request state and timing are often more useful than a stored conversation. Activity exposes sanitized lifecycle information; Insights estimates usage and cost from available upstream usage and the owner's price catalog.

Those cost figures are not an invoice. Missing usage and incorrect prices do not become accurate because a dashboard displays them. Short reachability tests and full large-context probes also call upstream providers and can consume paid tokens.

![Activity view; all timings and usage shown are fictional demo values, not benchmark results](https://raw.githubusercontent.com/GofMan5/luxury-switchboard/main/docs/screenshots/activity.png)

## What “local” does and does not mean

Configuration is stored locally with OS-backed encryption: DPAPI or the desktop keyring, without a plaintext fallback. Request history uses local SQLite. There is no hosted Luxury Switchboard control plane.

However, this is not an offline application:

- configured providers receive their requests and credentials;
- catalog discovery and model tests contact upstream providers;
- update checks/downloads contact GitHub;
- connecting a Codex account uses OpenAI sign-in/token endpoints;
- the optional public tunnel uses Cloudflare.

Loopback access also does not isolate the relay from other processes on the same machine.

The tunnel is optional and has separate published routes and an access key. Enabling it lets key holders generate traffic on the owner's provider accounts. The quick-tunnel address changes on restart. Leave it stopped for a local-only workflow.

Local guardrails are another bounded tool, not a guarantee of safe output. Monitor mode is the default; rules can miss problems or flag legitimate material.

## Current limits and useful feedback

The Windows installer is not Authenticode-signed. Release assets include SHA256SUMS.txt so you can check that a download matches the published file; this does not independently authenticate the publisher.

The repository screenshots use fictional providers and demo values. They are UI illustrations, not measured latency or cost comparisons. The app manages model access; users still need their own provider accounts and must follow their providers' terms.

You can find the source, installers, and contribution instructions in [Luxury Switchboard on GitHub](https://github.com/GofMan5/luxury-switchboard).

Useful bug reports include the app version, OS, client, API dialect, and a minimal reproduction with fictional data. Do not attach API keys, sign-in JSON, exported backups, or private prompts.

Which client workflow would you most want a reproducible compatibility guide for: simple chat, streaming, tools, or images?

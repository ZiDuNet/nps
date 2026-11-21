---
name: nps
description: >-
  Operate a NPS Pro (ZiDuNet/nps) control plane. Use it when the user wants to
  deploy an NPC client, list or manage clients, tunnels, domain reverse proxies,
  users, audit logs, or obtain the /mcp integration snippet for a running NPS
  server.
---

# NPS Pro Skill

NPS Pro is an intranet-penetration (内网穿透) server. This skill covers two
workflows: deploying the NPC client and driving the server through `npsctl` or
the `/mcp` endpoint.

## When to use

- The user asks to deploy NPC on a machine, register it as a system service, or
  add a second machine with another VKey.
- The user asks to list / create / update / delete clients, tunnels or hosts.
- The user asks for dashboard status, audit events, or an MCP configuration.

## Credentials

Ask the user for the panel base URL and one credential. Never invent, print, or
commit a real credential.

| Credential | Create / view in | `npsctl` | `/mcp` | Scope |
| --- | --- | --- | --- | --- |
| User API Token (`npsu_...`) | 接入与安全 | `--token` or `NPS_TOKEN` | `Authorization: Bearer ...` | That user's clients, tunnels, hosts and scoped audit records |
| Managed admin key (`npsa_...`) | 接入与安全, administrator only | `--key` or `NPS_KEY` with timestamp signing | `Authorization: Bearer ...` | Global |
| `auth_key` from `nps.conf` | Server configuration | `--key` or `NPS_KEY` with timestamp signing | Not accepted directly | Global |

The managed admin key is accepted by `npsctl --key` through the same signed
timestamp protocol as `auth_key`, and is also accepted directly as an MCP
Bearer token. `auth_key` itself cannot be used as a Bearer token. MCP audit is
scoped for ordinary users and global for administrators. `nps_users`,
`nps_global_config` and `nps_client_set_status` remain administrator-only.

## Deploying NPC

Prefer the exact command copied from the panel's client detail page. Do not
assume that `8024` or `8025` is correct: administrators can change listeners,
disable transports, use a path or ALPN, or publish different addresses.

The authoritative endpoint data is the client's `bridgeEndpoints` field in
`npsctl clients` / `/api/v1/clients`, or the copyable commands in the panel.
Only when the server still uses untouched defaults are the legacy TCP bridge
`8024` and TLS bridge `8025`.

```bash
# Use the exact bridge host/port/path/TLS fields returned by the server.
npc -server=<bridge_host>:<bridge_port> -vkey=<vkey>
npc -type=tls -tls_enable=true -server=<tls_host>:<tls_port> -vkey=<vkey>
```

Inside the NPC interactive menu, option `[1]` registers a system service. For
multiple machines using the same VKey, run NPC on each machine; the server
shows each live instance separately.

See `scripts/npc-help.sh` for a quick reference.

## Driving the server with npsctl

```bash
npsctl --help
npsctl --server https://nps.example.com --token npsu_xxx clients --limit 20
npsctl --server https://nps.example.com --token npsu_xxx client get 3
npsctl --server https://nps.example.com --token npsu_xxx mcp
```

List endpoints return `status`, `code`, `msg`, `rows`, `total`, `meta` and
`request_id`. `rows` is the current page; `--limit` does not fetch every page.
Create/update commands require `--data <JSON>`. `--token` and `--key` are
mutually exclusive, unknown trailing arguments are rejected, and `--server`
must be an absolute HTTP(S) URL.

If the user has no CLI, they can install it from the panel's 接入与安全 page,
or use the GitHub Release command. Never ask the user to run an unexplained
`cp -r` command: the installation command must download the published archive
or binary for them.

## MCP access

The server exposes a Streamable HTTP MCP endpoint at `/mcp`. Use
`"type": "http"`; do not configure it as `sse`.

```json
{
  "mcpServers": {
    "nps": {
      "type": "http",
      "url": "https://nps.example.com/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

Run `npsctl mcp` to generate the same snippet with the current server URL and
credential. The endpoint is stateless, so clients do not need to persist or
send `Mcp-Session-Id`. `GET /mcp` returns `405 Method Not Allowed`; use panel
or `/api/v1/info` for health checks. If an MCP client only supports stdio,
bridge it with `mcp-remote` rather than changing the URL to an SSE endpoint.

Read tools: `nps_dashboard`, `nps_clients`, `nps_client_get`, `nps_tunnels`,
`nps_hosts`, `nps_users`, `nps_audit`, `nps_global_config`.

Maintenance tools: `nps_client_create`, `nps_client_update`,
`nps_client_delete`, `nps_client_set_status`, `nps_tunnel_create`,
`nps_tunnel_delete`, `nps_tunnel_set_status`, `nps_host_create`,
`nps_host_delete`, `nps_host_set_status`.

There are no MCP `start` / `stop` tools; use `nps_*_set_status` with
`{"status": true|false}`. Ordinary users see only audit events in their own
scope; administrators see the global journal. List tools accept `offset` and
`limit` and return the matching `total`.

## HTTPS / custom CA

Use the correct public HTTPS URL in production. If Node.js-based MCP clients
use a private CA, set `NODE_EXTRA_CA_CERTS=/path/to/ca.pem` before starting the
client. `curl -k` is only for a temporary diagnostic and must not be copied
into MCP configuration.

## Safe operating rules

- Read before write: list resources and confirm the target ID and owner.
- Do not rotate or regenerate tokens unless the user explicitly asks; rotation
  invalidates the old token immediately.
- Do not delete a client, tunnel, host, audit file or log file without explicit
  confirmation.
- Prefer the panel-generated NPC command over handbuilding one from defaults.
- Treat server URLs, tokens, VKeys, private keys and full audit logs as secrets.

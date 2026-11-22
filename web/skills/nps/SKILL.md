---
name: nps
description: >-
  Operate a NPS Pro (ZiDuNet/nps) control plane. Use it when the user wants to
  deploy an NPC client, list or manage clients, tunnels, domain reverse proxies,
  users, audit logs, or obtain the /mcp integration snippet for a running NPS
  server.
metadata:
  version: "1.1.52"
  license: "GPL-3.0-or-later"
  source: "https://github.com/ZiDuNet/nps/tree/master/web/skills/nps"
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
| Managed admin key (`npsa_...`) | 接入与安全, administrator only | `--key` or `NPS_KEY` with HMAC-SHA256 signing | `Authorization: Bearer ...` | Global |
| `auth_key` from `nps.conf` | Server configuration | `--key` or `NPS_KEY` with HMAC-SHA256 signing | Not accepted directly | Global |

The managed admin key is accepted by `npsctl --key` through the same HMAC
protocol as `auth_key`, and is also accepted directly as an MCP
Bearer token. `auth_key` itself cannot be used as a Bearer token. MCP audit is
scoped for ordinary users and global for administrators. `nps_users`,
`nps_global_config` and `nps_client_set_status` remain administrator-only.

The administrator CLI signing and the default `api_hmac_enabled` behavior
require the NPS server release that supports HMAC (introduced in v1.1.51).
The v1.1.50 and earlier `npsctl` releases use legacy MD5 query signing and are not compatible with a
server where `api_legacy_md5_enabled` is false. Upgrade both server and CLI
together; only temporarily enable the legacy setting when an older integration
must be migrated, then turn it off again.

Prefer a user API Token for agent access because it is limited to that user's
resources and audit scope. The managed admin key grants global MCP access,
including cross-user resource maintenance and global user/audit/config reads.
Do not choose or copy the admin key by default; use it only when the user
specifically needs global access and confirms that scope. MCP does not provide
user-management or global-configuration write tools.

## Deploying NPC

Prefer the exact command copied from the panel's client detail page. Do not
assume that `8024` or `8025` is correct: administrators can change listeners,
disable transports, use a path or ALPN, or publish different addresses.

The authoritative endpoint data is the top-level `bridgeEndpoints` field in
the `npsctl clients` / `/api/v1/clients` response. It describes all active
Bridge listeners on the server; it is not a field on an individual client row
and does not identify a client-specific endpoint. The panel's copyable command
is the authoritative ready-to-run command for a particular client. Only when
the server still uses untouched defaults are the legacy TCP bridge `8024` and
TLS bridge `8025`.

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

List commands display a readable table by default. Add `--json` for the full
response envelope (including `rows`, `total`, pagination metadata and
`request_id`), or `--fields id,remark,status` for selected TSV columns. Lists
are paginated: for example, use `--limit 20 --offset 20` to fetch the second
page. Create/update commands require `--data <JSON>`. `--token` and `--key` are
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
credential. The endpoint is stateless: clients do not need to persist or send
`Mcp-Session-Id`, and stale or unknown session IDs are ignored. When MCP is
enabled, missing or invalid Bearer credentials receive `401` before method
handling; an authenticated `GET /mcp` receives `405 Method Not Allowed` because
streaming is disabled. If MCP is disabled, `/mcp` returns `404`.

For a basic public API liveness/discovery check, request `GET /api/v1/` (or
`<web_base_url>/api/v1/` when a Web base path is configured). It returns the
API version and capability document. `/api/v1/time` is also public but is
intended for clock synchronization. There is no `/api/v1/info` endpoint. If an
MCP client only supports stdio, bridge it with `mcp-remote` rather than
changing the URL to an SSE endpoint.

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

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

Both `npsctl` and `/mcp` need a credential:

- User API token (`npsu_...`) — scoped to one account. Use `Authorization: Bearer <token>`.
- Administrator `auth_key` — global; `npsctl` signs requests with `md5(auth_key + unix_timestamp)`.

Ask the user for the server URL and credential; never invent them.

## Deploying NPC

Prefer the official binary or the interactive menu:

```bash
npc -server=<host>:<bridge_port> -vkey=<vkey>
# TLS bridge:
npc -type=tls -tls_enable=true -server=<host>:<tls_port> -vkey=<vkey>
```

Inside the NPC interactive menu, option `[1]` registers a system service. For
multiple machines using the same VKey, run NPC on each machine; the server
shows each live instance separately.

See `scripts/npc-help.sh` for a quick reference.

## Driving the server with npsctl

```bash
npsctl --server http://127.0.0.1:8081 --token npsu_xxx clients
npsctl --server http://127.0.0.1:8081 --token npsu_xxx client get 3
npsctl --server http://127.0.0.1:8081 --token npsu_xxx mcp
```

Use `npsctl <command>` for the full command list. If the user has no CLI yet,
print the `/mcp` configuration (below) or guide them to the account page
"接入与安全".

## MCP access

The server exposes a streamable HTTP MCP endpoint at `/mcp`. Configure the
client with:

```json
{
  "mcpServers": {
    "nps": {
      "url": "http://127.0.0.1:8081/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

Available MCP tools: `nps_dashboard`, `nps_clients`, `nps_client_get`,
`nps_tunnels`, `nps_hosts`, `nps_users`, `nps_audit`, `nps_global_config`, plus
create/update/delete and start/stop tools for clients, tunnels and hosts.

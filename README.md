# NPS <sub>v1.1.39</sub>

[![](https://img.shields.io/github/v/release/ZiDuNet/nps.svg)](https://github.com/ZiDuNet/nps/releases)
[![](https://img.shields.io/github/stars/ZiDuNet/nps.svg)](https://github.com/ZiDuNet/nps/stargazers)
[![](https://img.shields.io/github/forks/ZiDuNet/nps.svg)](https://github.com/ZiDuNet/nps/network/members)
[![](https://img.shields.io/docker/pulls/wushuo98/nps.svg)](https://hub.docker.com/r/wushuo98/nps)

NPS is a lightweight, high-performance **intranet penetration** proxy server supporting **TCP/UDP forwarding, HTTP(S) reverse proxy, HTTP/SOCKS mixed proxy and P2P tunneling**, with a modern web management panel.

Administrators can create users and assign multiple clients to one user; ordinary users can create clients within their administrator-defined quota. Client expiry cannot exceed the owning user expiry, and a blank client expiry follows the user.

## Screenshots

| Login | IP registration |
| :---: | :---: |
| ![NPS login page](docs/assets/screenshots/login.jpg) | ![IP registration page](docs/assets/screenshots/ip-register.jpg) |
| **Client editing** | **Domain reverse proxy editing** |
| ![Client editing page](docs/assets/screenshots/client-edit.jpg) | ![Domain reverse proxy editing page](docs/assets/screenshots/host-edit.jpg) |

## Features

- 🚀 **Comprehensive Protocols** — TCP, UDP, HTTP(S), SOCKS5, P2P, Secret, File access
- 🖥️ **Cross-platform** — Linux / Windows / macOS / ARM / Synology, with one-click system service installation
- 🎨 **Web UI** — Operations-focused console with light/dark theme, responsive layout, Chinese/English localization, keyboard-friendly controls, and real-time traffic monitoring
- 👥 **User Management** — One user can manage multiple clients, with user-level tunnel quotas and expiration
- 🔒 **Security** — Random password on first start, IP whitelist/blacklist, CAPTCHA, rate limiting
- 🌐 **Domain Proxy** — Custom headers, URL routing, platform wildcard domain pools, route diagnostics, auto HTTPS and certificate hot reload
- 🔐 **TLS Encryption** — TLS encrypted communication between client and server
- 📦 **Docker** — Multi-arch images (amd64/arm/arm64), one-command deployment
- 🔌 **Open API** — Versioned `/api/v1` for clients, tunnels, hosts, users, global settings and audit, with administrator signatures and scoped user tokens
- 💻 **GUI Client** — Wails-based desktop client for Windows/macOS/Linux desktop environments

## Quick Start

### Server

```bash
# Run directly
./nps

# Install as system service
./nps install
nps start

# Docker
docker run -d --name nps \
  -p 80:80 -p 443:443 \
  -p 8024:8024 -p 8025:8025 \
  -p 127.0.0.1:8081:8081 \
  -e NPS_WEB_IP=0.0.0.0 \
  -v /opt/nps/conf:/conf \
  wushuo98/nps
```

New installations bind the panel to loopback. For the Docker example above, open `http://127.0.0.1:8081` on the host or put an HTTPS reverse proxy in front of the loopback port. The username is `admin`, and a random password is printed in the terminal on first launch. Known `CHANGE_ME` and historical weak template values are rotated automatically; explicit custom or empty values are preserved (except the historical shared `public_vkey=123`, which is disabled).

<details>
<summary>📋 Port Reference</summary>

| Port | Purpose |
|------|---------|
| 80 | HTTP reverse proxy |
| 443 | HTTPS reverse proxy |
| 8024 | Bridge TCP (client connections) |
| 8025 | Bridge TLS (encrypted connections) |
| 8081 | Web management panel |

</details>

### Client

```bash
# Interactive mode (recommended)
./npc

# Command line
./npc -server=<IP>:8024 -vkey=<key>

# TLS mode
./npc -server=<IP>:8025 -vkey=<key> -tls_enable=true

# Pin the self-signed server certificate (fingerprint is printed by nps)
./npc -server=<IP>:8025 -vkey=<key> -tls_enable=true \
  -tls_fingerprint=<SHA-256 fingerprint>

# Docker
docker run -d --name npc \
  wushuo98/npc -server=<IP>:8024 -vkey=<key>
```

> 💡 **Recommended**: Delete the `conf` folder under the npc directory to use config-free mode. All settings are managed via the server's web panel.

The Bridge supports TCP, KCP, WebSocket, WebSocket over TLS (WS/WSS), and QUIC; optional KCP, WS, WSS, and QUIC listeners use separate ports. TLS Bridge uses its own TLS port. QUIC Bridge is an encrypted UDP Bridge entry point, not a browser-facing HTTP/3 domain reverse proxy. Reusing one TCP port for Bridge, HTTP(S), and the management panel is limited to the existing protocol-routing mode.
TLS clients verify certificates by default. Use `-tls_ca_file` and `-tls_server_name` for a CA-backed certificate; do not enable `-tls_insecure_skip_verify=true` in production.

## Tunnel Modes

| Mode | Description | Use Cases |
|------|-------------|-----------|
| **TCP** | TCP port forwarding with load balancing | SSH, Remote Desktop, Databases |
| **UDP** | UDP port forwarding | DNS, Gaming, VoIP |
| **HTTP(S)** | Domain-based reverse proxy | WeChat dev, Web apps |
| **HTTP / SOCKS mixed proxy** | HTTP forward proxy and SOCKS5 on one public port | Browser proxy and full intranet access |
| **P2P** | Peer-to-peer penetration | Direct device connection |
| **Secret** | Private proxy | Secure temporary connections |
| **File** | Intranet file access | File browsing & download |

## Project Structure

```
nps/
├── cmd/
│   ├── nps/           # Server entry point
│   ├── npc/           # Client entry point
│   └── npc/npc-gui/   # Wails GUI client
├── bridge/            # Bridge layer (connection mgmt, tunnel mux)
├── server/            # Server core (proxy mode implementations)
├── client/            # Client core
├── lib/               # Shared libraries
│   ├── file/          # Data models + JSON persistence
│   ├── conn/          # Connection protocol
│   ├── nps_mux/       # Multiplexing library
│   ├── rate/          # Rate limiter
│   └── crypt/         # TLS certificate management
├── web/               # Web panel (Beego)
├── conf/              # Config + data storage
├── docs/              # Documentation site (VuePress)
├── build.sh           # Cross-platform build script
├── Makefile           # Build / test / CI
└── Dockerfile.*       # Docker builds
```

## Build from Source

Requires Go 1.26+

```bash
# Quick build
go build ./cmd/nps       # Server
go build ./cmd/npc       # Client

# Makefile (recommended)
make build                 # Build nps + npc
make test                  # Test with race detection & coverage
make lint                  # golangci-lint

# Cross compile
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" ./cmd/nps
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" ./cmd/npc

# GUI client (requires Wails 3)
cd cmd/npc/npc-gui
wails3 build --tags npcgui
```

## Docker

### Docker Hub

- **Server**: [wushuo98/nps](https://hub.docker.com/r/wushuo98/nps)
- **Client**: [wushuo98/npc](https://hub.docker.com/r/wushuo98/npc)

### docker-compose

```bash
git clone https://github.com/ZiDuNet/nps.git
cd nps
docker-compose up -d
```

## Documentation

- 📖 [Full Documentation](docs/README.md)
- 📦 [Installation](docs/install.md)
- 🚀 [Getting Started](docs/start.md)
- ▶️ [Command Reference](docs/run.md)
- 👥 [User Management](docs/user.md)
- 🧭 [In-App Usage Guide Configuration](docs/usage-guide.md)
- ⚙️ [Server Configuration Reference](docs/server/server_config.md)
- 📱 [Client Usage](docs/client/use.md)
- 🧾 [NPC Configuration File Reference](docs/client/config-file.md)
- 🔧 [Tunnel Details](docs/tunnel.md)
- 🐳 [Docker Deployment](docs/docker.md)
- 🖥️ [GUI Client](docs/gui.md)
- 🌐 [Platform Domains & Certificate Reload](docs/extend/platform-domain.md)
- 🔌 [External API v1](docs/api.md)
- 🔐 [API Authentication & Compatibility](docs/extend/api.md)
- 🧾 [Historical Web API](docs/extend/webapi.md)
- ⬆️ [Migration Guide](docs/migrate.md)

## Changelog

See [CHANGELOG.md](CHANGELOG.md) for full history.

### Recent

- **v1.1.39** (2026-10-30) - Refined list identities, cascading owner/client filters, compact tunnel targets, and clearer domain and access actions. Client lists now keep both traffic directions visible, show remarks and owner context in centered two-line cells, and expose HTTP/HTTPS entry buttons without losing external-link cues.

- **v1.1.38** (2026-10-29) - Unified list-page toolbars and expanded client connection audit details with server-side addresses, Bridge channels, handshake stages, client versions, public/local address provenance, account state and expiry information. Added terminal-style raw JSON detail views, public-table migrations for login bans and IP registration, login-ban filtering and pagination, and fixed scoped server-configuration saves and response-card layout.

- **v1.1.37** (2026-10-29) - Added protocol-aware Bridge command discovery for TCP, TLS, KCP, WS, WSS, QUIC and HTTP/3, with compatible v2 quick commands and multi-protocol NPC/GUI support. Added shared user quotas, connection audit logs, an independent certificate settings page, stricter custom-domain certificate APIs, and a redesigned registration and administration experience.

- **v1.1.36** (2026-10-29) - Rebuilt self-registration around the independent user model with configurable client/tunnel quotas, expiry, CAPTCHA, rate limits and optional SMTP email verification. Added the administrator registration API, account-scoped IP registration management, configurable in-app usage documentation, a searchable/source-aware server configuration editor, and automatic default Geo data loading. Existing configuration, Bridge compatibility and NPC versions remain unchanged.

- **v1.1.35** (2026-10-28) - Fixed the blank API access page shown to ordinary users. Ordinary accounts can now open `/user/api` and manage their own token again, while administrative user-management, global-settings, and administrator-key endpoints remain protected. Added a regression test for the non-admin page/action allowlist and added login, IP registration, client editing, and domain reverse-proxy editing screenshots to the README.

- **v1.1.34** (2026-10-26) - Reworked list details and form layout: the host list drops the authentication column in favor of a lock icon beside the public entry, the expanded host and TCP tunnel details use compact grouped cards, and the platform wildcard input in global settings is single-column with a bounded width. Fixed the host add/edit form being blocked by browser validation when no health-check target was configured.

- **v1.1.33** (2026-10-26) - Deleting a client in the web console now sends a "registration removed" notice to online NPCs; new NPCs close that connection and stop reconnecting while older NPCs keep their previous behavior. Service-mode NPCs only stop the affected connection, and only stop the local NPC service (keeping its registration, without uninstalling) when the last connection in the process is removed. Fixed the MUX shutdown blocking/file-descriptor leak, missing CA certificates in Docker images, and unified the browser favicon.

- **v1.1.32** (2026-10-26) - Added the unified `/api/v1` external API covering clients, tunnels, hosts, users, global configuration, Geo data, IP registration, audit, status and topology; it uses administrator signatures or scoped user Bearer tokens, a uniform response envelope, JSON request bodies and bounded pagination. The never-released `/v1/auth/*` aliases were removed; legacy `/auth/*` and all existing management endpoints remain unchanged.

- **v1.1.31** (2026-10-26) - Enabled real TCP/UDP forwarding for Secret and P2P target types with datagram framing, added Secret fallback for P2P visitors, tunnel-level connection and traffic quotas, certificate upload/paste/validation, the API access page with reusable tokens, online GeoIP/Geosite updates, an expanded dashboard, and a modernized Bootstrap 5 web console; fixed config-file public-vkey registration and the P2P direct QUIC handshake panic.

- **v1.1.30** (2026-10-25) - Completed NPC custom DNS, outbound proxies (HTTP/HTTPS, SOCKS4/4a/5, and environment proxy settings), local source-IP binding, and remote-target forwarding; documented QUIC/KCP source binding while preserving compatibility with older clients and configuration files.

- **v1.1.29** (2026-10-24) - Added modern P2P with legacy-client compatibility, secure Bridge challenge-response, HTTP/3 domain proxy support, optional ACME certificates, GeoIP/Geosite ACL data, NTP clock sync, Secret fallback, server configuration maintenance, and Web-configured backend health checks and per-resource directional limits.

- **v1.1.28** (2026-10-22) - Added safe Basic/legacy client username visibility and clarified domain proxy CORS and compatibility settings in the Web UI.
- **v1.1.27** (2026-10-22) - Fixed NPC service log permissions and completed CLI/GUI/server-triggered update flows; release packages are checksum-verified before replacement.
- **v1.1.26** (2026-10-22) - Restored the v1.1.24 feature baseline and the v1.1.23 release pipeline; master builds publish latest plus immutable SHA Docker tags, while formal releases publish versioned images.
- **v1.1.25** (2026-10-22) - Release tags now run CI, docs deployment, Docker publishing, and GitHub Release together; release notes are taken from the changelog
- **v1.1.24** (2026-10-21) - Added self-service client deletion; Docker `latest` now tracks formal releases while master test images use SHA tags
- **v1.1.23** (2026-10-21) - Added administrator-triggered NPC remote updates from the client list; releases are verified before replacement and automatic restart

- **v1.1.22** (2026-10-15) - Release build for the latest upstream compatibility fixes
- **v1.1.21** (2026-10-12) - Fixed reverse-proxy CAPTCHA paths, service-mode TLS certificate paths, SOCKS5 UDP relay address selection, and automatic service restart after server updates
- **v1.1.20** (2026-09-22) - Persisted encrypted dashboard shortcut keys for authorized re-viewing and moved user API token controls into the status column
- **v1.1.19** (2026-09-22) - Added self-service user API tokens, revocation, and scoped read-only resource topology shortcut keys for administrators and users
- **v1.1.17** (2026-09-21) - Improved release reliability with retried Linux GUI dependency downloads and Wails builds
- **v1.1.16** (2026-09-21) - Added operation audit logs, scoped per-user API tokens, and modern Host proxy options with bounded response-header rewriting
- **v1.1.15** (2026-09-20) - Fixed MUX close handling so Host responses and TCP streams drain queued trailing data before release
- **v1.1.14** (2026-09-20) - Rebuilt the resource topology screen from the user-dashboard prototype and opened it in a new tab from the console
- **v1.1.13** (2026-09-20) - Added an account-scoped, live resource topology view for clients, tunnels, and domain hosts
- **v1.1.12** (2026-09-19) - Corrected directional traffic and quota accounting, clarified tunnel runtime state, and added active TCP connection counts
- **v1.1.10** (2026-09-10) - Fixed Linux NPC system-service installation to use a stable installed binary path
- **v1.1.9** (2026-09-09) - Prevented Host proxy responses from being truncated when clients close the HTTP request side
- **v1.1.8** (2026-09-04) - User client quotas, expiry-bound validation, self-service client settings, and transparent brand assets
- **v1.1.6** (2026-09-02) - Release metadata synchronized for the refreshed console UI, platform domains, certificate hot reload, and GitHub publishing
- **v1.1.5** (2026-09-02) - Scoped runtime dashboard, proxy rates, quota and health status, flicker-free refresh, and streaming regression fixes
- **v1.1.4** (2026-09-02) - Platform wildcard domain pools, resilient certificate reload, route diagnostics, and console form refresh
- **v1.1.3** (2026-09-01) - Release pipeline, updater compatibility, and console layout fixes
- **v1.1.2** (2026-06-23) - Stable tunnel form switching, Docker build cache optimization
- **v1.1.1** (2026-06-15) — User management, multi-client ownership, user tunnel limits
- **v1.1.0** (2026-06-10) — Web UI modernization, bug fixes, security hardening
- **v1.0.0** (2026-05) — Secondary development baseline, traffic stats fix, UI redesign

## Contributing

Issues and Pull Requests are welcome!

1. Fork the repo
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

[GPL-3.0](LICENSE)

## Acknowledgments

Based on the original NPS project. Current project repository: [ZiDuNet/nps](https://github.com/ZiDuNet/nps).

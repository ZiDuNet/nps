# NPS <sub>v1.1.2</sub>

[![](https://img.shields.io/github/v/release/ZiDuNet/nps.svg)](https://github.com/ZiDuNet/nps/releases)
[![](https://img.shields.io/github/stars/ZiDuNet/nps.svg)](https://github.com/ZiDuNet/nps/stargazers)
[![](https://img.shields.io/github/forks/ZiDuNet/nps.svg)](https://github.com/ZiDuNet/nps/network/members)
[![](https://img.shields.io/docker/pulls/wushuo98/nps.svg)](https://hub.docker.com/r/wushuo98/nps)

NPS is a lightweight, high-performance **intranet penetration** proxy server supporting **TCP/UDP forwarding, HTTP(S) reverse proxy, SOCKS5 proxy and P2P tunneling**, with a modern web management panel.

Based on the original nps 0.26.10, with extensive bug fixes, performance and security improvements, and a redesigned Web UI. Since v1.1.1, administrators can create users and assign multiple clients to one user.

## Features

- 🚀 **Comprehensive Protocols** — TCP, UDP, HTTP(S), SOCKS5, P2P, Secret, File access
- 🖥️ **Cross-platform** — Linux / Windows / macOS / ARM / Synology, with one-click system service installation
- 🎨 **Web UI** — Operations-focused console with light/dark theme, responsive layout, Chinese/English localization, keyboard-friendly controls, and real-time traffic monitoring
- 👥 **User Management** — One user can manage multiple clients, with user-level tunnel quotas and expiration
- 🔒 **Security** — Random password on first start, IP whitelist/blacklist, CAPTCHA, rate limiting
- 🌐 **Domain Proxy** — Custom headers, 404 pages, host rewrite, URL routing, wildcard, auto HTTPS
- 🔐 **TLS Encryption** — TLS encrypted communication between client and server
- 📦 **Docker** — Multi-arch images (amd64/arm/arm64), one-command deployment
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

The Bridge transport currently supports TCP and KCP; TLS is exposed on the separate TLS Bridge port. QUIC and WebSocket Bridge transports are not supported yet.
TLS clients verify certificates by default. Use `-tls_ca_file` and `-tls_server_name` for a CA-backed certificate; do not enable `-tls_insecure_skip_verify=true` in production.

## Tunnel Modes

| Mode | Description | Use Cases |
|------|-------------|-----------|
| **TCP** | TCP port forwarding with load balancing | SSH, Remote Desktop, Databases |
| **UDP** | UDP port forwarding | DNS, Gaming, VoIP |
| **HTTP(S)** | Domain-based reverse proxy | WeChat dev, Web apps |
| **SOCKS5** | SOCKS5 proxy | Full intranet access |
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
├── docs/              # Documentation site (Docsify)
├── build.sh           # Cross-platform build script
├── Makefile           # Build / test / CI
└── Dockerfile.*       # Docker builds
```

## Build from Source

Requires Go 1.24+

```bash
# Quick build
go build cmd/nps/nps.go    # Server
go build cmd/npc/npc.go    # Client

# Makefile (recommended)
make build                 # Build nps + npc
make test                  # Test with race detection & coverage
make lint                  # golangci-lint

# Cross compile
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" ./cmd/nps/nps.go
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" ./cmd/npc/npc.go

# GUI client (requires Wails)
cd cmd/npc/npc-gui && wails build
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
- ⚙️ [Server Config](docs/server_config.md)
- 📱 [Client Config](docs/client_config.md)
- 🔧 [Tunnel Details](docs/tunnel.md)
- 🐳 [Docker Deployment](docs/docker.md)
- 🖥️ [GUI Client](docs/gui.md)
- ⬆️ [Migration Guide](docs/migrate.md)

## Changelog

See [CHANGELOG.md](CHANGELOG.md) for full history.

### Recent

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

# NPS <sub>v1.1.1</sub>

[![](https://img.shields.io/github/v/release/ZiDuNet/nps.svg)](https://github.com/ZiDuNet/nps/releases)
[![](https://img.shields.io/github/stars/ZiDuNet/nps.svg)](https://github.com/ZiDuNet/nps/stargazers)
[![](https://img.shields.io/github/forks/ZiDuNet/nps.svg)](https://github.com/ZiDuNet/nps/network/members)
[![](https://img.shields.io/docker/pulls/wushuo98/nps.svg)](https://hub.docker.com/r/wushuo98/nps)

NPS is a lightweight, high-performance **intranet penetration** proxy server supporting **TCP/UDP forwarding, HTTP(S) reverse proxy, SOCKS5 proxy and P2P tunneling**, with a modern web management panel.

Based on the original nps 0.26.10, with extensive bug fixes, performance and security improvements, and a redesigned Web UI. Since v1.1.1, administrators can create users and assign multiple clients to one user.

## Features

- 🚀 **Comprehensive Protocols** — TCP, UDP, HTTP(S), SOCKS5, P2P, Secret, File access
- 🖥️ **Cross-platform** — Linux / Windows / macOS / ARM / Synology, with one-click system service installation
- 🎨 **Web UI** — Modern interface with light/dark theme, real-time traffic and speed monitoring
- 👥 **User Management** — One user can manage multiple clients, with user-level tunnel quotas and expiration
- 🔒 **Security** — Random password on first start, IP whitelist/blacklist, CAPTCHA, rate limiting
- 🌐 **Domain Proxy** — Custom headers, 404 pages, host rewrite, URL routing, wildcard, auto HTTPS
- 🔐 **TLS Encryption** — TLS encrypted communication between client and server
- 📦 **Docker** — Multi-arch images (amd64/arm/arm64), one-command deployment
- 💻 **GUI Client** — Wails-based desktop client (Windows)

## Quick Start

### Server

```bash
# Run directly
./nps

# Install as system service (interactive)
./nps -server

# Docker
docker run -d --name nps \
  -p 80:80 -p 443:443 \
  -p 8024:8024 -p 8080:8080 \
  -v /opt/nps/conf:/conf \
  wushuo98/nps
```

After starting, visit `http://<server-ip>:8080` to access the web panel. A random username and password will be printed in the terminal on first launch.

<details>
<summary>📋 Port Reference</summary>

| Port | Purpose |
|------|---------|
| 80 | HTTP reverse proxy |
| 443 | HTTPS reverse proxy |
| 8024 | Bridge TCP (client connections) |
| 8025 | Bridge TLS (encrypted connections) |
| 8080 | Web management panel |

</details>

### Client

```bash
# Interactive mode (recommended)
./npc

# Command line
./npc -server=<IP>:8024 -vkey=<key>

# TLS mode
./npc -server=<IP>:8025 -vkey=<key> -tls_enable=true

# Docker
docker run -d --name npc \
  wushuo98/npc -server=<IP>:8024 -vkey=<key>
```

> 💡 **Recommended**: Delete the `conf` folder under the npc directory to use config-free mode. All settings are managed via the server's web panel.

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
- 🚀 [Getting Started](docs/start.md)
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

Based on [ehang-io/nps](https://github.com/ehang-io/nps). Thanks to the original author.

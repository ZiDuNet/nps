// Package bridgeendpoint centralizes the public view of every enabled Bridge
// listener. Listener definitions live in nps.conf; this package only applies
// enablement, display overrides and the public-address fallback rules.
package bridgeendpoint

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/astaxie/beego"
)

type Endpoint struct {
	Key           string `json:"key"`
	Protocol      string `json:"protocol"`
	Transport     string `json:"transport"`
	Label         string `json:"label"`
	Host          string `json:"host,omitempty"`
	Port          int    `json:"port"`
	Address       string `json:"address,omitempty"`
	Path          string `json:"path,omitempty"`
	ALPN          string `json:"alpn,omitempty"`
	TLSServerName string `json:"tlsServerName,omitempty"`
	TLS           bool   `json:"tls"`
	Enabled       bool   `json:"enabled"`
	Show          bool   `json:"show"`
	Main          bool   `json:"main"`
	UsesUDP       bool   `json:"usesUdp"`
	ViaHTTP3      bool   `json:"viaHttp3,omitempty"`
}

// AvailableForCommands reports whether a valid copyable endpoint can be
// generated. show=false deliberately keeps an enabled listener out of client
// commands while it remains visible in server status.
func (e Endpoint) AvailableForCommands() bool {
	return e.Enabled && e.Show && e.Port > 0 && strings.TrimSpace(e.Host) != ""
}

type DisplayEndpoint struct {
	Show bool
	Host string
	Port int
	ALPN string
}

type Config struct {
	RequestHost string
	ServerURL   string
	BridgeAddr  string
	BridgeIP    string
	BridgeHost  string
	BridgeType  string
	BridgePath  string

	BridgePort     int
	TLSBridgePort  int
	KCPBridgePort  int
	WSBridgePort   int
	WSSBridgePort  int
	QUICBridgePort int
	HTTP3ProxyPort int
	QUICALPN       string

	TLSEnabled  bool
	HTTP3Bridge bool

	DisplayPath string
	TCP         DisplayEndpoint
	TLS         DisplayEndpoint
	KCP         DisplayEndpoint
	WS          DisplayEndpoint
	WSS         DisplayEndpoint
	QUIC        DisplayEndpoint
	HTTP3       DisplayEndpoint
}

// FromAppConfig reads the listener and display settings from the active Beego
// configuration. It never mutates nps.conf.
func FromAppConfig(requestHost, serverURL string) Config {
	quicALPN := strings.TrimSpace(beego.AppConfig.String("quic_alpn"))
	if quicALPN == "" {
		quicALPN = "nps"
	}
	bridgeType := strings.ToLower(strings.TrimSpace(beego.AppConfig.String("bridge_type")))
	if bridgeType == "" {
		bridgeType = "tcp"
	}
	bridgePath := strings.TrimSpace(beego.AppConfig.String("bridge_path"))
	if bridgePath == "" {
		bridgePath = "/ws"
	}
	bridgePort := beego.AppConfig.DefaultInt("bridge_port", 0)
	tlsPort := beego.AppConfig.DefaultInt("tls_bridge_port", 8025)
	kcpPort := beego.AppConfig.DefaultInt("bridge_kcp_port", 0)
	wsPort := beego.AppConfig.DefaultInt("bridge_ws_port", 0)
	wssPort := beego.AppConfig.DefaultInt("bridge_wss_port", 0)
	quicPort := beego.AppConfig.DefaultInt("bridge_quic_port", 0)
	tlsEnabled := beego.AppConfig.DefaultBool("tls_enable", false)
	return Config{
		RequestHost:    requestHost,
		ServerURL:      serverURL,
		BridgeAddr:     strings.TrimSpace(beego.AppConfig.String("bridge_addr")),
		BridgeIP:       strings.TrimSpace(beego.AppConfig.String("bridge_ip")),
		BridgeHost:     strings.TrimSpace(beego.AppConfig.String("bridge_host")),
		BridgeType:     bridgeType,
		BridgePath:     bridgePath,
		BridgePort:     bridgePort,
		TLSBridgePort:  tlsPort,
		KCPBridgePort:  kcpPort,
		WSBridgePort:   wsPort,
		WSSBridgePort:  wssPort,
		QUICBridgePort: quicPort,
		HTTP3ProxyPort: beego.AppConfig.DefaultInt("http3_proxy_port", 0),
		QUICALPN:       quicALPN,
		TLSEnabled:     tlsEnabled,
		HTTP3Bridge:    beego.AppConfig.DefaultBool("bridge_http3", false),
		DisplayPath:    strings.TrimSpace(beego.AppConfig.String("bridge_show_path")),
		TCP:            displayFromConfig("tcp", bridgePort, tlsEnabled && bridgeType == "tcp"),
		TLS:            displayFromConfig("tls", tlsPort, tlsEnabled && bridgeType == "tcp"),
		KCP:            displayFromConfig("kcp", kcpPort, bridgeType == "kcp" || kcpPort > 0),
		WS:             displayFromConfig("ws", wsPort, wsPort > 0),
		WSS:            displayFromConfig("wss", wssPort, wssPort > 0),
		QUIC: func() DisplayEndpoint {
			display := displayFromConfig("quic", quicPort, quicPort > 0)
			if display.ALPN == "" {
				display.ALPN = quicALPN
			}
			return display
		}(),
		HTTP3: DisplayEndpoint{
			Show: beego.AppConfig.DefaultBool("bridge_http3", false),
			Port: beego.AppConfig.DefaultInt("http3_proxy_port", 0),
		},
	}
}

func displayFromConfig(key string, actualPort int, enabled bool) DisplayEndpoint {
	return DisplayEndpoint{
		Show: beego.AppConfig.DefaultBool("bridge_"+key+"_show", enabled),
		Host: strings.TrimSpace(beego.AppConfig.String("bridge_" + key + "_show_ip")),
		Port: beego.AppConfig.DefaultInt("bridge_"+key+"_show_port", actualPort),
		ALPN: strings.TrimSpace(beego.AppConfig.String("bridge_" + key + "_show_alpn")),
	}
}

// Resolve returns every actually enabled listener in stable UI order. The
// first endpoint is the configured main listener, with TCP remaining the
// default when bridge_type is absent.
func Resolve(config Config) []Endpoint {
	bridgeType := strings.ToLower(strings.TrimSpace(config.BridgeType))
	if bridgeType == "" {
		bridgeType = "tcp"
	}
	baseHost := publicHost(config.RequestHost, config.ServerURL, config.BridgeAddr)
	path := strings.TrimSpace(config.DisplayPath)
	if path == "" {
		path = config.BridgePath
	}
	build := func(key, protocol, transport, label string, port int, enabled, tls, usesUDP, viaHTTP3 bool, display DisplayEndpoint, endpointPath, alpn, tlsServerName string) Endpoint {
		host := strings.TrimSpace(display.Host)
		if host == "" {
			host = baseHost
		}
		displayPort := display.Port
		if displayPort <= 0 {
			displayPort = port
		}
		return Endpoint{
			Key:           key,
			Protocol:      protocol,
			Transport:     transport,
			Label:         label,
			Host:          host,
			Port:          displayPort,
			Address:       joinAddress(host, displayPort),
			Path:          endpointPath,
			ALPN:          alpn,
			TLSServerName: tlsServerName,
			TLS:           tls,
			Enabled:       enabled && port > 0,
			Show:          display.Show,
			Main:          key == bridgeType,
			UsesUDP:       usesUDP,
			ViaHTTP3:      viaHTTP3,
		}
	}

	endpoints := make([]Endpoint, 0, 7)
	tcp := build("tcp", "tcp", "tcp", "TCP", config.BridgePort,
		bridgeType == "tcp", false, false, false, config.TCP, "", "", "")
	endpoints = append(endpoints, tcp)

	tlsEndpoint := build("tls", "tls", "tcp", "TCP + TLS", config.TLSBridgePort,
		bridgeType == "tcp" && config.TLSEnabled, true, false, false, config.TLS, "", "", "")
	endpoints = append(endpoints, tlsEndpoint)

	kcpEnabled := bridgeType == "kcp" || config.KCPBridgePort > 0
	kcpPort := config.KCPBridgePort
	if bridgeType == "kcp" {
		kcpPort = config.BridgePort
	}
	kcp := build("kcp", "kcp", "kcp", "KCP", kcpPort,
		kcpEnabled, false, true, false, config.KCP, "", "", "")
	endpoints = append(endpoints, kcp)

	ws := build("ws", "ws", "ws", "WS", config.WSBridgePort,
		config.WSBridgePort > 0, false, false, false, config.WS, path, "", "")
	endpoints = append(endpoints, ws)

	wss := build("wss", "wss", "wss", "WSS", config.WSSBridgePort,
		config.WSSBridgePort > 0, true, false, false, config.WSS, path, "", "")
	endpoints = append(endpoints, wss)

	// bridge_host is the intended TLS identity when the public listener is
	// multiplexed by hostname. Direct TCP/TLS and WSS endpoints do not need an
	// explicit server name, which keeps their historical commands compatible.
	tlsIdentity := strings.TrimSpace(config.BridgeHost)
	if tlsIdentity == "" {
		tlsIdentity = baseHost
	}
	quicALPN := config.QUICALPN
	if quicALPN == "" {
		quicALPN = "nps"
	}
	if config.QUIC.ALPN != "" {
		quicALPN = config.QUIC.ALPN
	}
	quic := build("quic", "quic", "quic", "QUIC", config.QUICBridgePort,
		config.QUICBridgePort > 0, true, true, false, config.QUIC, "", quicALPN, tlsIdentity)
	endpoints = append(endpoints, quic)

	http3ALPN := config.QUICALPN
	if http3ALPN == "" {
		http3ALPN = "nps"
	}
	http3 := build("http3", "http3", "quic", "HTTP/3 (Bridge)", config.HTTP3ProxyPort,
		config.HTTP3Bridge, true, true, true, config.HTTP3, "", http3ALPN, tlsIdentity)
	endpoints = append(endpoints, http3)
	return endpoints
}

// Enabled returns only listening endpoints and is used by server status.
func Enabled(endpoints []Endpoint) []Endpoint {
	result := make([]Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint.Enabled {
			result = append(result, endpoint)
		}
	}
	return result
}

// Available returns endpoints that are both listening and exposed for command
// generation.
func Available(endpoints []Endpoint) []Endpoint {
	result := make([]Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint.AvailableForCommands() {
			result = append(result, endpoint)
		}
	}
	return result
}

func joinAddress(host string, port int) string {
	if port <= 0 || strings.TrimSpace(host) == "" {
		return ""
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
}

func publicHost(requestHost, serverURL, bridgeAddr string) string {
	for _, candidate := range []string{bridgeAddr, serverURL, requestHost} {
		if host := normalizeHost(candidate); host != "" {
			return host
		}
	}
	return ""
}

func normalizeHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return strings.Trim(host, "[]")
	}
	if ip := net.ParseIP(strings.Trim(value, "[]")); ip != nil {
		return ip.String()
	}
	parseValue := value
	if !strings.Contains(parseValue, "://") {
		parseValue = "//" + parseValue
	}
	parsed, err := url.Parse(parseValue)
	if err != nil {
		return ""
	}
	host := parsed.Hostname()
	if host == "" {
		return ""
	}
	return strings.Trim(host, "[]")
}

// ProtocolName maps an actual transport recorded by Bridge to the label used
// by the management console.
func ProtocolName(transport string) string {
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "tcp":
		return "TCP"
	case "tls":
		return "TCP + TLS"
	case "kcp":
		return "KCP"
	case "ws":
		return "WS"
	case "wss":
		return "WSS"
	case "quic":
		return "QUIC"
	case "http3":
		return "HTTP/3"
	default:
		if strings.TrimSpace(transport) == "" {
			return ""
		}
		return strings.ToUpper(strings.TrimSpace(transport))
	}
}

// Describe provides a compact status line for logs and APIs.
func (e Endpoint) Describe() string {
	if e.Address == "" {
		return fmt.Sprintf("%s :%d", e.Label, e.Port)
	}
	return fmt.Sprintf("%s %s", e.Label, e.Address)
}

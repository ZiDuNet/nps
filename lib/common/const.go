package common

const (
	CONN_DATA_SEQ  = "*#*" //Separator
	VERIFY_EER     = "vkey"
	VERIFY_SUCCESS = "sucs"
	WORK_MAIN      = "main"
	WORK_CHAN      = "chan"
	WORK_CONFIG    = "conf"
	WORK_REGISTER  = "rgst"
	WORK_SECRET    = "sert"
	WORK_FILE      = "file"
	WORK_P2P       = "p2pm"
	// WORK_P2P_SESSION is the provider-side control stream used by the
	// modern hole-punch protocol.  Older clients never send this value and
	// continue to use the historical UDP/KCP P2P flow.
	WORK_P2P_SESSION  = "p2sj"
	WORK_P2P_VISITOR  = "p2pv"
	WORK_P2P_PROVIDER = "p2pp"
	WORK_P2P_CONNECT  = "p2pc"
	WORK_P2P_SUCCESS  = "p2ps"
	WORK_P2P_END      = "p2pe"
	WORK_P2P_LAST     = "p2pl"
	WORK_STATUS       = "stus"
	RES_MSG           = "msg0"
	RES_CLOSE         = "clse"
	NEW_UDP_CONN      = "udpc" //p2p udp conn
	CLIENT_UPDATE     = "updt" //server asks the client to run its built-in updater
	P2P_PUNCH_START   = "p2st" //server asks the provider NPC to join a P2P session
	CONN_ACK          = "ACK"  //optional Secret/P2P metadata acknowledgement
	// CLIENT_CLOSE is sent on the WORK_MAIN channel right before the server
	// removes a client registration. New NPCs stop their reconnect loop when
	// they see it; older NPCs ignore the flag and rely on the subsequent
	// connection close, preserving their historical reconnect behaviour.
	CLIENT_CLOSE = "kick"
	// HEALTH_CONFIG carries the effective health-check profiles for a client.
	// It is sent only to NPC versions that explicitly understand this flag so
	// older clients keep the historical control-stream protocol unchanged.
	HEALTH_CONFIG = "hlth"
	// REPORT_LOCAL_IP: server requests client local/private IPs on WORK_MAIN.
	// New clients reply with WriteLenContent; old clients ignore the flag (server times out).
	REPORT_LOCAL_IP = "rlip"
	NEW_TASK        = "task"
	NEW_CONF        = "conf"
	NEW_HOST        = "host"
	CONN_TCP        = "tcp"
	CONN_UDP        = "udp"
	// CONN_ALL is the stored target_type that lets a Secret/P2P visitor send
	// both TCP streams and UDP datagrams over the same task.
	CONN_ALL          = "all"
	CONN_TEST         = "TST"
	UnauthorizedBytes = `HTTP/1.1 401 Unauthorized
Content-Type: text/plain; charset=utf-8
WWW-Authenticate: Basic realm="easyProxy"

401 Unauthorized`
	// ProxyAuthRequiredBytes is the challenge for HTTP forward proxy Basic auth (RFC 7235).
	// Clients expect 407 + Proxy-Authenticate, not 401 + WWW-Authenticate.
	ProxyAuthRequiredBytes = "HTTP/1.1 407 Proxy Authentication Required\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Proxy-Authenticate: Basic realm=\"nps\"\r\n" +
		"\r\n" +
		"407 Proxy Authentication Required"
	ConnectionFailBytes = `HTTP/1.1 404 Not Found

`
	Unauthorized = `HTTP/1.1 401 Unauthorized

`
)

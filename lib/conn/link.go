package conn

import (
	"time"

	cryptlib "ehang.io/nps/lib/crypt"
)

type Secret struct {
	Password string
	Conn     *Conn
}

func NewSecret(p string, conn *Conn) *Secret {
	return &Secret{
		Password: p,
		Conn:     conn,
	}
}

type Link struct {
	ConnType     string //连接类型
	Host         string //目标
	Crypt        bool   //加密
	Compress     bool
	LocalProxy   bool
	RemoteAddr   string
	ProtoVersion string
	// TLSFingerprint is supplied by the server on links that use the optional
	// inner TLS layer. The client received the value over the authenticated
	// bridge and can pin the same certificate for defense in depth.
	TLSFingerprint string
	Option         Options
}

type Option func(*Options)

type Options struct {
	Timeout time.Duration
}

var defaultTimeOut = time.Second * 5

func NewLink(connType string, host string, cryptEnabled bool, compress bool, remoteAddr string, localProxy bool, protoVersion string, opts ...Option) *Link {
	options := newOptions(opts...)

	return &Link{
		RemoteAddr:     remoteAddr,
		ConnType:       connType,
		Host:           host,
		Crypt:          cryptEnabled,
		Compress:       compress,
		LocalProxy:     localProxy,
		ProtoVersion:   protoVersion,
		TLSFingerprint: cryptlib.GetCertFingerprint(),
		Option:         options,
	}
}

func newOptions(opts ...Option) Options {
	opt := Options{
		Timeout: defaultTimeOut,
	}
	for _, o := range opts {
		o(&opt)
	}
	return opt
}

func LinkTimeout(t time.Duration) Option {
	return func(opt *Options) {
		opt.Timeout = t
	}
}

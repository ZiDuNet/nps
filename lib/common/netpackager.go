package common

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
)

type NetPackager interface {
	Pack(writer io.Writer) (err error)
	UnPack(reader io.Reader) (err error)
}

const (
	ipV4       = 1
	domainName = 3
	ipV6       = 4
)

type UDPHeader struct {
	Rsv  uint16
	Frag uint8
	Addr *Addr
}

func NewUDPHeader(rsv uint16, frag uint8, addr *Addr) *UDPHeader {
	return &UDPHeader{
		Rsv:  rsv,
		Frag: frag,
		Addr: addr,
	}
}

type Addr struct {
	Type uint8
	Host string
	Port uint16
}

func (addr *Addr) String() string {
	return net.JoinHostPort(addr.Host, strconv.Itoa(int(addr.Port)))
}

func (addr *Addr) Decode(b []byte) error {
	addr.Type = b[0]
	pos := 1
	switch addr.Type {
	case ipV4:
		addr.Host = net.IP(b[pos : pos+net.IPv4len]).String()
		pos += net.IPv4len
	case ipV6:
		addr.Host = net.IP(b[pos : pos+net.IPv6len]).String()
		pos += net.IPv6len
	case domainName:
		addrlen := int(b[pos])
		pos++
		addr.Host = string(b[pos : pos+addrlen])
		pos += addrlen
	default:
		return errors.New("decode error")
	}

	addr.Port = binary.BigEndian.Uint16(b[pos:])

	return nil
}

func (addr *Addr) Encode(b []byte) (int, error) {
	b[0] = addr.Type
	pos := 1
	switch addr.Type {
	case ipV4:
		ip4 := net.ParseIP(addr.Host).To4()
		if ip4 == nil {
			ip4 = net.IPv4zero.To4()
		}
		pos += copy(b[pos:], ip4)
	case domainName:
		b[pos] = byte(len(addr.Host))
		pos++
		pos += copy(b[pos:], []byte(addr.Host))
	case ipV6:
		ip16 := net.ParseIP(addr.Host).To16()
		if ip16 == nil {
			ip16 = net.IPv6zero.To16()
		}
		pos += copy(b[pos:], ip16)
	default:
		b[0] = ipV4
		copy(b[pos:pos+4], net.IPv4zero.To4())
		pos += 4
	}
	binary.BigEndian.PutUint16(b[pos:], addr.Port)
	pos += 2

	return pos, nil
}

func (h *UDPHeader) Write(w io.Writer) error {
	b := BufPoolUdp.Get().([]byte)
	defer BufPoolUdp.Put(b)

	binary.BigEndian.PutUint16(b[:2], h.Rsv)
	b[2] = h.Frag

	addr := h.Addr
	if addr == nil {
		addr = &Addr{}
	}
	length, _ := addr.Encode(b[3:])

	_, err := w.Write(b[:3+length])
	return err
}

type UDPDatagram struct {
	Header *UDPHeader
	Data   []byte
}

func ReadUDPDatagram(r io.Reader) (*UDPDatagram, error) {
	// Read the fixed prefix first, then grow the packet as needed. The old
	// implementation used the MTU-sized UDP pool for the complete datagram;
	// valid SOCKS5 UDP payloads can be larger than one Ethernet frame and must
	// not be truncated (or panic while slicing the fixed buffer).
	prefix := make([]byte, 5)
	if _, err := io.ReadFull(r, prefix); err != nil {
		return nil, err
	}
	if len(prefix) < 5 {
		return nil, io.ErrUnexpectedEOF
	}
	var raw []byte
	raw = append(raw, prefix...)

	header := &UDPHeader{
		Rsv:  binary.BigEndian.Uint16(prefix[:2]),
		Frag: prefix[2],
	}

	// when r is a streaming (such as TCP connection), callers use the
	// reserved length field to delimit the packet. For a regular SOCKS5 UDP
	// datagram the reader already contains exactly one packet, so consume the
	// remainder without imposing the old MTU-sized limit.
	atype := prefix[3]
	hlen := 0
	switch atype {
	case ipV4:
		hlen = 10
	case ipV6:
		hlen = 22
	case domainName:
		hlen = 7 + int(prefix[4])
	default:
		return nil, errors.New("addr not support")
	}
	dlen := int(header.Rsv)
	if dlen == 0 {
		extra, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		raw = append(raw, extra...)
	} else {
		need := hlen + dlen - len(raw)
		if need < 0 {
			return nil, errors.New("invalid UDP data length")
		}
		extra := make([]byte, need)
		if _, err := io.ReadFull(r, extra); err != nil {
			return nil, err
		}
		raw = append(raw, extra...)
	}
	if len(raw) < hlen {
		return nil, io.ErrUnexpectedEOF
	}
	header.Addr = new(Addr)
	if err := header.Addr.Decode(raw[3:hlen]); err != nil {
		return nil, err
	}
	data := make([]byte, len(raw)-hlen)
	copy(data, raw[hlen:])
	return &UDPDatagram{Header: header, Data: data}, nil
}

func NewUDPDatagram(header *UDPHeader, data []byte) *UDPDatagram {
	return &UDPDatagram{
		Header: header,
		Data:   data,
	}
}

func (d *UDPDatagram) Write(w io.Writer) error {
	h := d.Header
	if h == nil {
		h = &UDPHeader{}
	}
	buf := bytes.Buffer{}
	if err := h.Write(&buf); err != nil {
		return err
	}
	if _, err := buf.Write(d.Data); err != nil {
		return err
	}

	_, err := buf.WriteTo(w)
	return err
}

func ToSocksAddr(addr net.Addr) *Addr {
	host := "0.0.0.0"
	port := 0
	if addr != nil {
		h, p, _ := net.SplitHostPort(addr.String())
		host = h
		port, _ = strconv.Atoi(p)
	}
	return &Addr{
		Type: ipV4,
		Host: host,
		Port: uint16(port),
	}
}

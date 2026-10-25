package client

import (
	"encoding/binary"
	"log"
	"os"

	"ehang.io/nps/lib/common"
)

func RegisterLocalIp(server string, vKey string, tp string, proxyUrl string, localIP string, hour int) {
	RegisterLocalIpWithTLS(server, vKey, tp, proxyUrl, hour, GetTlsEnable(), TLSOptions{LocalIP: localIP})
}

// RegisterLocalIpWithTLS keeps the registration command aligned with the same
// transport, DNS, source-address, and certificate settings used by NPC.
func RegisterLocalIpWithTLS(server string, vKey string, tp string, proxyUrl string, hour int, tlsEnabled bool, options TLSOptions) {
	if err := common.SetCustomDNS(options.DNSServer); err != nil {
		log.Printf("[配置] DNS 服务器配置无效，继续使用系统解析器：%v", err)
	}
	c, err := NewConnWithTLS(tp, vKey, server, common.WORK_REGISTER, proxyUrl, tlsEnabled, options)
	if err != nil {
		log.Fatalln(err)
	}
	if err := binary.Write(c, binary.LittleEndian, int32(hour)); err != nil {
		log.Fatalln(err)
	}
	log.Printf("Successful ip registration for local public network, the validity period is %d hours.", hour)
	os.Exit(0)
}

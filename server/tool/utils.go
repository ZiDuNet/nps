package tool

import (
	"math"
	"math/rand"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/common"
	"github.com/astaxie/beego"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
)

var (
	ports             []int
	ServerStatus      []map[string]interface{}
	ServerStatusLock  sync.RWMutex
	ServerStatusMu    sync.RWMutex
	IORateCache       atomic.Value
)

func StartSystemInfo() {
	if b, err := beego.AppConfig.Bool("system_info_display"); err == nil && b {
		ServerStatus = make([]map[string]interface{}, 0, 1500)
		go getSeverStatus()
	}
}

func InitAllowPort() {
	p := beego.AppConfig.String("allow_ports")
	ports = common.GetPorts(p)
}

func TestServerPort(p int, m string) (b bool) {
	if m == "p2p" || m == "secret" {
		return true
	}
	if p > 65535 || p < 0 {
		return false
	}
	if len(ports) != 0 {
		if !common.InIntArr(ports, p) {
			return false
		}
	}
	if m == "udp" {
		b = common.TestUdpPort(p)
	} else {
		b = common.TestTcpPort(p)
	}
	return
}

func GenerateServerPort(m string) int {
	for i := 0; i < 1000; i++ {
		//生成随机数 1024 - 65535
		serverPort := rand.Intn(65535)
		if serverPort < 1024 {
			serverPort = 1024
		}

		if TestServerPort(serverPort, m) {
			return serverPort
		}
	}
	return 0
}

func getSeverStatus() {
	for {
		if len(ServerStatus) < 10 {
			time.Sleep(time.Second)
		} else {
			time.Sleep(time.Minute)
		}
		cpuPercet, _ := cpu.Percent(0, true)
		var cpuAll float64
		for _, v := range cpuPercet {
			cpuAll += v
		}
		m := make(map[string]interface{})
		loads, _ := load.Avg()
		m["load1"] = math.Round(loads.Load1*100) / 100
		m["load5"] = loads.Load5
		m["load15"] = loads.Load15
		m["cpu"] = math.Round(cpuAll / float64(len(cpuPercet)))
		swap, _ := mem.SwapMemory()
		m["swap_mem"] = math.Round(swap.UsedPercent)
		vir, _ := mem.VirtualMemory()
		m["virtual_mem"] = math.Round(vir.UsedPercent)
		conn, _ := net.ProtoCounters(nil)
		// 从后台 IO 采集缓存获取
		if ioData, ok := IORateCache.Load().(map[string]interface{}); ok {
			m["io_send"] = ioData["io_send"]
			m["io_recv"] = ioData["io_recv"]
		}
		t := time.Now()
		m["time"] = strconv.Itoa(t.Hour()) + ":" + strconv.Itoa(t.Minute()) + ":" + strconv.Itoa(t.Second())

		for _, v := range conn {
			m[v.Protocol] = v.Stats["CurrEstab"]
		}
		ServerStatusMu.Lock()
		if len(ServerStatus) >= 1440 {
			ServerStatus = ServerStatus[1:]
		}
		ServerStatusLock.Lock()
		ServerStatus = append(ServerStatus, m)
		ServerStatusLock.Unlock()
		ServerStatusMu.Unlock()
	}
}

// StartIORateCollector 启动后台 IO 速率采集协程
func StartIORateCollector() {
	go collectIORate()
}

func collectIORate() {
	// 初始化缓存，避免空值 panic
	IORateCache.Store(map[string]interface{}{
		"io_send": uint64(0),
		"io_recv": uint64(0),
	})
	for {
		io1, _ := net.IOCounters(false)
		time.Sleep(time.Second)
		io2, _ := net.IOCounters(false)
		if len(io2) > 0 && len(io1) > 0 {
			IORateCache.Store(map[string]interface{}{
				"io_send": io2[0].BytesSent - io1[0].BytesSent,
				"io_recv": io2[0].BytesRecv - io1[0].BytesRecv,
			})
		}
	}
}

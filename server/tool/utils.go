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
	ports            []int
	ServerStatus     []map[string]interface{}
	ServerStatusLock sync.RWMutex
	// ServerStatusMu is retained for source compatibility. ServerStatusLock is
	// the single lock used by the collector and dashboard readers.
	ServerStatusMu sync.RWMutex
	IORateCache    atomic.Value
)

const serverStatusHistoryLimit = 1440

func StartSystemInfo() {
	if b, err := beego.AppConfig.Bool("system_info_display"); err == nil && b {
		ServerStatusLock.Lock()
		ServerStatus = make([]map[string]interface{}, 0, serverStatusHistoryLimit)
		ServerStatusLock.Unlock()
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
		if serverStatusCount() < 10 {
			time.Sleep(time.Second)
		} else {
			time.Sleep(time.Minute)
		}
		appendServerStatus(GetSystemStatus())
	}
}

// GetSystemStatus returns a complete dashboard snapshot. Every field consumed by
// the dashboard is initialized first so an unsupported system counter cannot
// leave invalid JavaScript in the rendered page.
func GetSystemStatus() map[string]interface{} {
	status := newSystemStatus(time.Now())

	if cpuPercent, err := cpu.Percent(0, true); err == nil && len(cpuPercent) > 0 {
		var cpuTotal float64
		for _, value := range cpuPercent {
			cpuTotal += value
		}
		status["cpu"] = math.Round(cpuTotal / float64(len(cpuPercent)))
	}
	if loads, err := load.Avg(); err == nil && loads != nil {
		status["load1"] = math.Round(loads.Load1*100) / 100
		status["load5"] = loads.Load5
		status["load15"] = loads.Load15
		status["load"] = loads.String()
	}
	if swap, err := mem.SwapMemory(); err == nil && swap != nil {
		status["swap_mem"] = math.Round(swap.UsedPercent)
	}
	if virtual, err := mem.VirtualMemory(); err == nil && virtual != nil {
		status["virtual_mem"] = math.Round(virtual.UsedPercent)
	}
	if counters, err := net.ProtoCounters(nil); err == nil {
		for _, counter := range counters {
			if established, ok := counter.Stats["CurrEstab"]; ok {
				status[counter.Protocol] = established
			}
		}
	}
	status["io_send"], status["io_recv"] = ioRates()
	return status
}

// GetServerStatusSamples returns evenly-spaced, independent history snapshots.
// It pads an empty history with zero-value samples so the dashboard charts can
// render safely while the collector is warming up.
func GetServerStatusSamples(count int) []map[string]interface{} {
	if count <= 0 {
		return nil
	}

	samples := make([]map[string]interface{}, count)
	ServerStatusLock.RLock()
	defer ServerStatusLock.RUnlock()

	if len(ServerStatus) == 0 {
		for index := range samples {
			samples[index] = newSystemStatus(time.Time{})
		}
		return samples
	}

	historyLen := len(ServerStatus)
	for index := range samples {
		statusIndex := 0
		if count > 1 && historyLen > 1 {
			// Use the endpoints as anchors so a dashboard refresh always includes
			// the newest observation, even when downsampling the ring buffer.
			statusIndex = index * (historyLen - 1) / (count - 1)
		}
		samples[index] = cloneSystemStatus(ServerStatus[statusIndex])
	}
	return samples
}

func newSystemStatus(now time.Time) map[string]interface{} {
	timestamp := ""
	if !now.IsZero() {
		timestamp = strconv.Itoa(now.Hour()) + ":" + strconv.Itoa(now.Minute()) + ":" + strconv.Itoa(now.Second())
	}
	return map[string]interface{}{
		"load":        `{"load1":0,"load5":0,"load15":0}`,
		"load1":       float64(0),
		"load5":       float64(0),
		"load15":      float64(0),
		"cpu":         float64(0),
		"swap_mem":    float64(0),
		"virtual_mem": float64(0),
		"io_send":     uint64(0),
		"io_recv":     uint64(0),
		"tcp":         int64(0),
		"udp":         int64(0),
		"time":        timestamp,
	}
}

func cloneSystemStatus(status map[string]interface{}) map[string]interface{} {
	clone := newSystemStatus(time.Time{})
	for key, value := range status {
		clone[key] = value
	}
	return clone
}

func serverStatusCount() int {
	ServerStatusLock.RLock()
	defer ServerStatusLock.RUnlock()
	return len(ServerStatus)
}

func appendServerStatus(status map[string]interface{}) {
	if status == nil {
		status = newSystemStatus(time.Now())
	}
	ServerStatusLock.Lock()
	defer ServerStatusLock.Unlock()
	if len(ServerStatus) >= serverStatusHistoryLimit {
		copy(ServerStatus, ServerStatus[len(ServerStatus)-serverStatusHistoryLimit+1:])
		ServerStatus = ServerStatus[:serverStatusHistoryLimit-1]
	}
	ServerStatus = append(ServerStatus, status)
}

func ioRates() (send, receive uint64) {
	if data, ok := IORateCache.Load().(map[string]interface{}); ok {
		if value, ok := data["io_send"].(uint64); ok {
			send = value
		}
		if value, ok := data["io_recv"].(uint64); ok {
			receive = value
		}
	}
	return send, receive
}

// StartIORateCollector 启动后台 IO 速率采集协程
func StartIORateCollector() {
	go collectIORate()
}

func collectIORate() {
	IORateCache.Store(map[string]interface{}{
		"io_send": uint64(0),
		"io_recv": uint64(0),
	})
	var previous []net.IOCountersStat
	for {
		current, err := net.IOCounters(false)
		if err != nil || len(current) == 0 {
			previous = nil
			time.Sleep(time.Second)
			continue
		}
		if len(previous) > 0 {
			IORateCache.Store(map[string]interface{}{
				"io_send": counterDelta(current[0].BytesSent, previous[0].BytesSent),
				"io_recv": counterDelta(current[0].BytesRecv, previous[0].BytesRecv),
			})
		}
		previous = current
		time.Sleep(time.Second)
	}
}

func counterDelta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

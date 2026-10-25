package common

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/astaxie/beego/logs"
)

const ntpEpochOffset = 2208988800

var ntpState struct {
	sync.RWMutex
	server   string
	interval time.Duration
	offset   time.Duration
	wake     chan struct{}
	running  bool
}

// ConfigureTimeSync starts or updates the optional background NTP worker.
// Failures keep the last known offset, and the worker never blocks the NPS/NPC
// connection or forwarding goroutines.
func ConfigureTimeSync(server string, interval time.Duration) {
	server = strings.TrimSpace(server)
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ntpState.Lock()
	ntpState.server = server
	ntpState.interval = interval
	if ntpState.wake == nil {
		ntpState.wake = make(chan struct{}, 1)
	}
	if !ntpState.running {
		ntpState.running = true
		go runTimeSync()
	}
	ntpState.Unlock()
	select {
	case ntpState.wake <- struct{}{}:
	default:
	}
}

// TimeNow returns wall clock time corrected by the latest successful NTP
// measurement. Until the first successful measurement it returns time.Now.
func TimeNow() time.Time {
	ntpState.RLock()
	offset := ntpState.offset
	ntpState.RUnlock()
	return time.Now().Add(offset)
}

// SetTimezone changes the process-local timezone used by timestamps. An empty
// value preserves the operating-system setting.
func SetTimezone(timezone string) error {
	if strings.TrimSpace(timezone) == "" {
		return nil
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return err
	}
	time.Local = location
	return nil
}

func runTimeSync() {
	for {
		ntpState.RLock()
		server, interval, wake := ntpState.server, ntpState.interval, ntpState.wake
		ntpState.RUnlock()
		if server == "" {
			<-wake
			continue
		}
		if offset, err := measureNTP(server); err != nil {
			logs.Warn("[时间同步] NTP 服务器 %s 校时失败，保留上次校准值：%v", server, err)
		} else {
			ntpState.Lock()
			ntpState.offset = offset
			ntpState.Unlock()
			if offset > time.Second || offset < -time.Second {
				logs.Info("[时间同步] NTP 服务器 %s 校准偏差 %s", server, offset.Round(time.Millisecond))
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-timer.C:
		case <-wake:
			timer.Stop()
		}
	}
}

func measureNTP(server string) (time.Duration, error) {
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(strings.Trim(server, "[]"), "123")
	}
	conn, err := net.DialTimeout("udp", server, 3*time.Second)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return 0, err
	}
	request := make([]byte, 48)
	request[0] = 0x23 // LI=0, VN=4, client mode.
	if _, err := conn.Write(request); err != nil {
		return 0, err
	}
	response := make([]byte, 48)
	n, err := conn.Read(response)
	if err != nil {
		return 0, err
	}
	serverTime, err := ntpResponseTime(response[:n])
	if err != nil {
		return 0, err
	}
	if serverTime.Year() < 2020 || serverTime.After(time.Now().Add(24*time.Hour)) {
		return 0, fmt.Errorf("NTP time outside acceptable range: %s", serverTime.UTC().Format(time.RFC3339))
	}
	return time.Until(serverTime), nil
}

func ntpResponseTime(response []byte) (time.Time, error) {
	if len(response) < 48 || response[0]&7 != 4 || response[1] == 0 {
		return time.Time{}, errors.New("invalid NTP server response")
	}
	seconds := int64(binary.BigEndian.Uint32(response[40:44])) - ntpEpochOffset
	nanos := int64(binary.BigEndian.Uint32(response[44:48])) * int64(time.Second) / (1 << 32)
	return time.Unix(seconds, nanos), nil
}

package client

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego/logs"
	"github.com/pkg/errors"
)

type healthReporter struct {
	conn *conn.Conn
	mu   sync.Mutex
}

func (r *healthReporter) send(info, status string) error {
	if r == nil || r.conn == nil {
		return errors.New("health connection is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.conn.SendHealthInfo(info, status)
	return err
}

func heathCheck(healths []*file.Health, c *conn.Conn, stop ...<-chan struct{}) bool {
	if c == nil || len(healths) == 0 {
		return false
	}
	validHealths := make([]*file.Health, 0, len(healths))
	now := time.Now()
	for _, health := range healths {
		if health == nil {
			continue
		}
		health.Lock()
		if health.HealthMaxFail > 0 && health.HealthCheckTimeout > 0 && health.HealthCheckInterval > 0 {
			health.HealthNextTime = now.Add(time.Duration(health.HealthCheckInterval) * time.Second)
			health.HealthMap = make(map[string]int)
			validHealths = append(validHealths, health)
		}
		health.Unlock()
	}
	if len(validHealths) == 0 {
		return false
	}
	var stopCh <-chan struct{}
	if len(stop) > 0 {
		stopCh = stop[0]
	}
	go session(validHealths, &healthReporter{conn: c}, stopCh)
	return true
}

func session(healths []*file.Health, reporter *healthReporter, stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			for _, health := range healths {
				if health == nil {
					continue
				}
				health.Lock()
				due := !health.HealthNextTime.After(now)
				if due {
					health.HealthNextTime = now.Add(time.Duration(health.HealthCheckInterval) * time.Second)
				}
				health.Unlock()
				if due {
					go check(health, reporter)
				}
			}
		case <-stop:
			return
		}
	}
}

// check performs one health pass. The reporter serializes writes because a
// client may have multiple health targets running concurrently.
func check(health *file.Health, reporter *healthReporter) {
	if health == nil || reporter == nil {
		return
	}
	arr := strings.Split(health.HealthCheckTarget, ",")
	var healthTimeout, maxFail int
	var healthType, healthURL string
	health.Lock()
	healthTimeout = health.HealthCheckTimeout
	maxFail = health.HealthMaxFail
	healthType = health.HealthCheckType
	healthURL = health.HttpHealthUrl
	if health.HealthMap == nil {
		health.HealthMap = make(map[string]int)
	}
	health.Unlock()

	for _, target := range arr {
		var checkErr error
		if healthType == "tcp" {
			var c net.Conn
			c, checkErr = net.DialTimeout("tcp", target, time.Duration(healthTimeout)*time.Second)
			if checkErr == nil {
				_ = c.Close()
			}
		} else {
			client := &http.Client{Timeout: time.Duration(healthTimeout) * time.Second}
			response, err := client.Get("http://" + target + healthURL)
			checkErr = err
			if response != nil {
				if response.Body != nil {
					_ = response.Body.Close()
				}
				if checkErr == nil && response.StatusCode != http.StatusOK {
					checkErr = errors.New("status code is not match")
				}
			}
		}

		var sendStatus string
		health.Lock()
		if checkErr != nil && maxFail > 0 {
			health.HealthMap[target]++
		} else if checkErr == nil && health.HealthMap[target] >= maxFail {
			// A previously failed target has recovered.
			sendStatus = "1"
			health.HealthMap[target] = 0
		}
		if checkErr != nil && maxFail > 0 && health.HealthMap[target] > 0 && health.HealthMap[target]%maxFail == 0 {
			sendStatus = "0"
		}
		health.Unlock()
		if sendStatus != "" {
			if err := reporter.send(target, sendStatus); err != nil {
				logs.Debug("send health status failed for %s: %v", target, err)
			}
		}
	}
}

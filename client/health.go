package client

import (
	"fmt"
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

func healthProfileKey(health *file.Health) string {
	if health == nil {
		return ""
	}
	snapshot := file.HealthConfigSnapshot(health)
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%d\x00%s",
		snapshot.HealthCheckType,
		snapshot.HealthCheckTarget,
		snapshot.HealthCheckTimeout,
		snapshot.HealthMaxFail,
		snapshot.HealthCheckInterval,
		snapshot.HttpHealthUrl,
	)
}

func normalizedHealthCopy(health *file.Health) *file.Health {
	if health == nil {
		return nil
	}
	snapshot := file.HealthConfigSnapshot(health)
	if err := file.NormalizeHealth(&snapshot); err != nil || !file.HealthConfigEnabled(&snapshot) {
		return nil
	}
	return &snapshot
}

// healthProfiles returns one isolated runtime profile per distinct local or
// server-provided configuration. Isolating the copies prevents a remote
// refresh from racing the counters maintained by an existing check session.
func (s *TRPClient) healthProfiles() []*file.Health {
	if s == nil {
		return nil
	}
	profiles := make([]*file.Health, 0)
	seen := make(map[string]struct{})
	appendProfile := func(source *file.Health) {
		profile := normalizedHealthCopy(source)
		if profile == nil {
			return
		}
		key := healthProfileKey(profile)
		if key == "" {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		profiles = append(profiles, profile)
	}
	if s.cnf != nil {
		for _, health := range s.cnf.Healths {
			appendProfile(health)
		}
	}
	for _, health := range s.remoteHealths {
		appendProfile(health)
	}
	return profiles
}

// stopHealthChecks stops the current check session. Closing this dedicated
// channel is separate from closeCh so a server-side configuration refresh can
// restart checks without ending the NPC process.
func (s *TRPClient) stopHealthChecks() {
	if s == nil {
		return
	}
	s.healthMu.Lock()
	if s.healthStop != nil {
		close(s.healthStop)
		s.healthStop = nil
	}
	s.healthMu.Unlock()
}

func (s *TRPClient) startHealthChecks(signal *conn.Conn) {
	if s == nil {
		return
	}
	s.healthMu.Lock()
	if s.healthStop != nil {
		close(s.healthStop)
		s.healthStop = nil
	}
	if signal == nil || s.closed.Load() {
		s.healthMu.Unlock()
		return
	}
	profiles := s.healthProfiles()
	if len(profiles) == 0 {
		s.healthMu.Unlock()
		return
	}
	stop := make(chan struct{})
	s.healthStop = stop
	s.healthMu.Unlock()
	go heathCheck(profiles, signal, stop)
}

func (s *TRPClient) applyRemoteHealthConfig(profiles []file.Health) {
	if s == nil {
		return
	}
	normalized := make([]*file.Health, 0, len(profiles))
	for i := range profiles {
		profile := profiles[i]
		if err := file.NormalizeHealth(&profile); err != nil || !file.HealthConfigEnabled(&profile) {
			continue
		}
		normalized = append(normalized, &profile)
	}
	s.healthMu.Lock()
	s.remoteHealths = normalized
	s.healthMu.Unlock()
	s.startHealthChecks(s.currentSignal())
}

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
		health.HealthLock()
		if health.HealthMaxFail > 0 && health.HealthCheckTimeout > 0 && health.HealthCheckInterval > 0 {
			health.HealthNextTime = now.Add(time.Duration(health.HealthCheckInterval) * time.Second)
			health.HealthMap = make(map[string]int)
			validHealths = append(validHealths, health)
		}
		health.HealthUnlock()
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
				health.HealthLock()
				due := !health.HealthNextTime.After(now)
				if due {
					health.HealthNextTime = now.Add(time.Duration(health.HealthCheckInterval) * time.Second)
				}
				health.HealthUnlock()
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
	health.HealthLock()
	healthTimeout = health.HealthCheckTimeout
	maxFail = health.HealthMaxFail
	healthType = health.HealthCheckType
	healthURL = health.HttpHealthUrl
	if health.HealthMap == nil {
		health.HealthMap = make(map[string]int)
	}
	health.HealthUnlock()

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
		health.HealthLock()
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
		health.HealthUnlock()
		if sendStatus != "" {
			if err := reporter.send(target, sendStatus); err != nil {
				logs.Debug("send health status failed for %s: %v", target, err)
			}
		}
	}
}

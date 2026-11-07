package install

import (
	"errors"
	"time"
)

type updateServicePhase int

const (
	updateServiceStopped updateServicePhase = iota
	updateServiceRunning
	updateServiceStartPending
	updateServiceStopPending
	updateServiceOther
)

type updateServiceOps struct {
	query func() (updateServicePhase, error)
	start func() error
	stop  func() error
}

// Start is idempotent, and deliberately NOT Restart (Stop then Start). A
// service that is already stopped must still be started. Pending stops are
// allowed to finish before issuing Start; stopped neighbours remain stopped.
func ensureUpdateService(ops updateServiceOps, start bool, timeout, stableFor, interval time.Duration) error {
	started, stopped := false, false
	stable := time.Time{}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		phase, err := ops.query()
		if err != nil {
			return err
		}
		if start {
			if phase == updateServiceStopped && !started {
				if err := ops.start(); err != nil {
					return err
				}
				started = true
				continue
			}
			if phase == updateServiceStopped && started {
				return errors.New("新服务启动后立即停止")
			}
			if phase == updateServiceRunning {
				if stable.IsZero() {
					stable = time.Now()
				}
				if time.Since(stable) >= stableFor {
					return nil
				}
			} else {
				stable = time.Time{}
			}
		} else {
			if phase == updateServiceStopped {
				return nil
			}
			if phase != updateServiceStopPending && !stopped {
				if err := ops.stop(); err != nil {
					return err
				}
				stopped = true
			}
		}
		time.Sleep(interval)
	}
	return errors.New("服务未在期限内进入稳定状态")
}

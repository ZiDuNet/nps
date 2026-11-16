package controllers

import (
	"sync"
	"time"

	install "ehang.io/nps/lib/install"
	npsversion "ehang.io/nps/lib/version"
)

// versionCheck caches the latest upstream release tag. The panel used to query
// api.github.com directly from the browser, which fails for deployments without
// GitHub reachability and burns the anonymous rate limit on every page load.
// The server performs one lookup per interval and every console shares it.
type versionCheckState struct {
	mu        sync.RWMutex
	latest    string
	checkedAt time.Time
	errMsg    string
}

var serverVersionCheck versionCheckState

const (
	versionCheckInterval        = 6 * time.Hour
	versionCheckFailureInterval = 15 * time.Minute
)

var (
	versionRefreshMu  sync.Mutex
	versionRefreshing bool
	versionWorkerOnce sync.Once
)

func cachedLatestRelease() (latest string, checkedAt time.Time, errMsg string) {
	serverVersionCheck.mu.RLock()
	latest, checkedAt, errMsg = serverVersionCheck.latest, serverVersionCheck.checkedAt, serverVersionCheck.errMsg
	serverVersionCheck.mu.RUnlock()
	return
}

func refreshLatestRelease() {
	latest, err := install.LatestVersion()
	serverVersionCheck.mu.Lock()
	defer serverVersionCheck.mu.Unlock()
	serverVersionCheck.checkedAt = time.Now()
	if err != nil {
		serverVersionCheck.errMsg = err.Error()
		return
	}
	serverVersionCheck.errMsg = ""
	serverVersionCheck.latest = latest
}

func versionCheckFresh(latest string, checkedAt time.Time, errMsg string) bool {
	if checkedAt.IsZero() {
		return false
	}
	interval := versionCheckInterval
	if errMsg != "" {
		interval = versionCheckFailureInterval
	}
	return time.Since(checkedAt) < interval
}

// EnsureVersionCheck refreshes the cached release in the background. A single
// in-flight request is shared by every caller, so a burst of console page
// loads cannot exhaust the GitHub anonymous rate limit.
func EnsureVersionCheck() {
	latest, checkedAt, errMsg := cachedLatestRelease()
	if versionCheckFresh(latest, checkedAt, errMsg) {
		return
	}
	versionRefreshMu.Lock()
	if versionRefreshing {
		versionRefreshMu.Unlock()
		return
	}
	versionRefreshing = true
	versionRefreshMu.Unlock()
	go func() {
		defer func() {
			versionRefreshMu.Lock()
			versionRefreshing = false
			versionRefreshMu.Unlock()
		}()
		refreshLatestRelease()
	}()
}

// StartVersionCheckWorker keeps the cached release tag fresh while the server
// runs. Exported for the server entrypoint; safe to call repeatedly.
func StartVersionCheckWorker() {
	versionWorkerOnce.Do(func() {
		EnsureVersionCheck()
		go func() {
			// Poll often enough to retry a failed lookup after the shorter
			// failure interval; successful checks are skipped as fresh.
			ticker := time.NewTicker(versionCheckFailureInterval)
			defer ticker.Stop()
			for range ticker.C {
				EnsureVersionCheck()
			}
		}()
	})
}

func versionCheckPayload() map[string]interface{} {
	latest, checkedAt, errMsg := cachedLatestRelease()
	result := map[string]interface{}{
		"status":     1,
		"current":    npsversion.VERSION,
		"latest":     latest,
		"checked_at": checkedAt.Unix(),
		"repository": npsversion.GitHubRepository,
	}
	if errMsg != "" {
		result["check_error"] = errMsg
	}
	if latest != "" {
		if comparison, err := npsversion.Compare(npsversion.VERSION, latest); err == nil {
			result["update_available"] = comparison < 0
		}
	}
	return result
}

// VersionCheckProxy serves the cached release state to the console topbar.
// BaseController.Prepare requires an authenticated session, so this does not
// leak the deployment's version to anonymous visitors.
func (s *GlobalController) VersionCheckProxy() {
	EnsureVersionCheck()
	s.Data["json"] = s.withRequestMeta(versionCheckPayload())
	s.ServeJSON()
}

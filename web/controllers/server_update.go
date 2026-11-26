package controllers

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	install "ehang.io/nps/lib/install"
	npsversion "ehang.io/nps/lib/version"
)

// serverUpdateJob tracks the single in-flight panel upgrade. The job lives in
// memory on purpose: a successful upgrade replaces the process image with the
// new binary, so the console detects completion by observing the new version
// after the panel reconnects instead of reading a persisted record.
type serverUpdateJob struct {
	Running    bool   `json:"running"`
	Stage      string `json:"stage"`
	Message    string `json:"message"`
	Target     string `json:"target"`
	Error      string `json:"error"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at"`
}

var serverUpdateState struct {
	mu  sync.RWMutex
	job serverUpdateJob
}

const serverUpdateJobTimeout = 15 * time.Minute

func currentServerUpdateJob() serverUpdateJob {
	serverUpdateState.mu.RLock()
	job := serverUpdateState.job
	serverUpdateState.mu.RUnlock()
	if job.Running && job.StartedAt > 0 && time.Since(time.Unix(job.StartedAt, 0)) > serverUpdateJobTimeout {
		return serverUpdateJob{
			Stage:      "failed",
			Message:    "升级任务超时，请检查服务日志",
			Error:      "升级任务超过 15 分钟未完成",
			StartedAt:  job.StartedAt,
			FinishedAt: time.Now().Unix(),
		}
	}
	return job
}

func updateServerUpdateJob(mutate func(*serverUpdateJob)) {
	serverUpdateState.mu.Lock()
	mutate(&serverUpdateState.job)
	serverUpdateState.mu.Unlock()
}

func beginServerUpdateJob(target string) bool {
	serverUpdateState.mu.Lock()
	defer serverUpdateState.mu.Unlock()
	if serverUpdateState.job.Running {
		return false
	}
	serverUpdateState.job = serverUpdateJob{
		Running:   true,
		Stage:     "starting",
		Message:   "正在准备升级",
		Target:    target,
		StartedAt: time.Now().Unix(),
	}
	return true
}

func setServerUpdateStage(stage, message string) {
	updateServerUpdateJob(func(job *serverUpdateJob) {
		job.Stage = stage
		job.Message = message
	})
}

func finishServerUpdateJob(stage, message string, err error) {
	updateServerUpdateJob(func(job *serverUpdateJob) {
		job.Running = false
		job.Stage = stage
		job.Message = message
		if err != nil {
			job.Error = err.Error()
		}
		job.FinishedAt = time.Now().Unix()
	})
}

type serverUpdateCapability struct {
	Supported   bool   `json:"supported"`
	Reason      string `json:"reason"`
	Environment string `json:"environment"`
	Executable  string `json:"-"`
}

// detectServerUpdateCapability decides whether the panel may replace and
// re-exec the running server. Windows cannot replace a running executable, and
// development builds live in a temporary directory that would disappear.
func detectServerUpdateCapability() serverUpdateCapability {
	exe, err := os.Executable()
	if err != nil {
		return serverUpdateCapability{Environment: serverUpdateEnvironment(), Reason: "无法定位当前程序: " + err.Error()}
	}
	return serverUpdateCapabilityFor(exe)
}

// serverUpdateCapabilityFor is the testable form: callers pass the executable
// path that would be replaced instead of relying on the test binary location.
func serverUpdateCapabilityFor(exe string) serverUpdateCapability {
	environment := serverUpdateEnvironment()
	if runtime.GOOS == "windows" {
		return serverUpdateCapability{
			Environment: environment,
			Reason:      "Windows 不支持在面板内替换正在运行的程序，请使用服务端菜单升级或手动替换",
		}
	}
	if strings.TrimSpace(exe) == "" {
		return serverUpdateCapability{Environment: environment, Reason: "无法定位当前程序"}
	}
	exe = filepath.Clean(exe)
	if resolved, resolveErr := filepath.EvalSymlinks(exe); resolveErr == nil {
		exe = resolved
	}
	if strings.Contains(exe, "go-build") {
		return serverUpdateCapability{
			Environment: environment,
			Reason:      "当前是开发或临时构建，请在正式安装目录中升级",
		}
	}
	dir := filepath.Dir(exe)
	probe, probeErr := os.CreateTemp(dir, ".nps-update-check-*")
	if probeErr != nil {
		return serverUpdateCapability{
			Environment: environment,
			Reason:      "当前程序所在目录不可写，请用有权限的账号运行或手动升级",
		}
	}
	probeName := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probeName)
	return serverUpdateCapability{Supported: true, Environment: environment, Executable: exe}
}

func serverUpdateEnvironment() string {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}
	// The installed unit always starts the binary with the "service" argument,
	// so its presence is a reliable signal without guessing from the parent PID.
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "service" {
			return "systemd"
		}
	}
	return "process"
}

func serverUpdatePayload() map[string]interface{} {
	EnsureVersionCheck()
	latest, checkedAt, errMsg := cachedLatestRelease()
	capability := detectServerUpdateCapability()
	payload := map[string]interface{}{
		"status":       1,
		"current":      npsversion.VERSION,
		"latest":       latest,
		"checked_at":   checkedAt.Unix(),
		"repository":   npsversion.GitHubRepository,
		"supported":    capability.Supported,
		"environment":  capability.Environment,
		"job":          currentServerUpdateJob(),
		"download_url": "https://github.com/" + npsversion.GitHubRepository + "/releases/latest",
	}
	if errMsg != "" {
		payload["check_error"] = errMsg
	}
	if !capability.Supported {
		payload["reason"] = capability.Reason
	}
	if latest != "" {
		if comparison, err := npsversion.Compare(npsversion.VERSION, latest); err == nil {
			payload["update_available"] = comparison < 0
		}
	}
	return payload
}

// ServerUpdate reports the cached release, whether this deployment can upgrade
// itself, and the current job. Any signed-in account may read it so the topbar
// can explain the update to non-administrators.
func (s *GlobalController) ServerUpdate() {
	s.Data["json"] = s.withRequestMeta(serverUpdatePayload())
	s.ServeJSON()
}

// ServerUpdateApply downloads, verifies and installs the latest server release,
// then re-execs the process so the new binary takes over. The response returns
// before the restart, so the console can render progress.
func (s *GlobalController) ServerUpdateApply() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	capability := detectServerUpdateCapability()
	if !capability.Supported {
		s.AjaxErr(capability.Reason)
		return
	}
	if !beginServerUpdateJob(capability.Executable) {
		s.AjaxErr("已有升级任务正在进行，请等待其完成")
		return
	}
	s.auditMutation("server.update", "nps", 0, 0, "", nil, nil)
	go runServerUpdate(capability.Executable)
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status": 1,
		"msg":    "已开始升级，服务会在下载校验完成后自动重启",
		"job":    currentServerUpdateJob(),
	})
	s.ServeJSON()
}

func runServerUpdate(exe string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			finishServerUpdateJob("failed", "升级过程中发生异常", fmt.Errorf("%v", recovered))
		}
	}()
	setServerUpdateStage("downloading", "正在下载并校验更新包，现有连接保持运行")
	staged, err := install.StageNpsUpdate(exe)
	if err != nil {
		finishServerUpdateJob("failed", "下载或校验更新包失败", err)
		return
	}
	if !staged.Downloaded {
		finishServerUpdateJob("completed", "当前已是最新版本", nil)
		return
	}
	// Keep the menu updater's own rollback file untouched; the web updater owns
	// a separate backup so both entrypoints can never clobber each other.
	staged.PreviousBinary = exe + install.ServerUpdateBackupSuffix
	setServerUpdateStage("installing", "正在替换程序文件")
	if err := staged.Install(); err != nil {
		staged.DiscardDownload()
		finishServerUpdateJob("failed", "替换程序文件失败，已保留原程序", err)
		return
	}
	staged.DiscardDownload()
	setServerUpdateStage("restarting", "正在以新版本重启，连接会短暂中断")
	// Give the HTTP response and the console poll a moment to complete before
	// the process image is replaced.
	time.Sleep(2 * time.Second)
	if err := execSelf(exe); err != nil {
		finishServerUpdateJob("failed", "程序已更新，但自动重启失败，请手动重启 NPS", err)
	}
}

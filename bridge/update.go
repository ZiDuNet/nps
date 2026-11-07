package bridge

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/connectionlog"
	"ehang.io/nps/lib/version"
	"github.com/astaxie/beego"
)

type ClientUpdateJob struct {
	JobID      string    `json:"job_id"`
	ClientID   int       `json:"client_id"`
	InstanceID string    `json:"instance_id,omitempty"`
	Current    string    `json:"current"`
	Target     string    `json:"target"`
	Stage      string    `json:"stage"`
	Message    string    `json:"message"`
	Legacy     bool      `json:"legacy"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Finished   bool      `json:"finished"`
}

func updateStageTerminal(stage string) bool {
	switch stage {
	case "completed", "failed", "unconfirmed", "no_update":
		return true
	}
	return false
}

func (s *Bridge) updateSession(id int) (*Client, error) {
	if s == nil {
		return nil, errors.New("Bridge 尚未初始化")
	}
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	v, ok := s.Client.Load(id)
	if !ok {
		return nil, errors.New("客户端当前离线")
	}
	root, ok := bridgeClient(v)
	if !ok {
		return nil, errors.New("客户端当前离线")
	}
	ensureClientSessions(root)
	var selected *Client
	for _, session := range root.sessions {
		if sessionHasResource(session, "signal") {
			if selected != nil {
				return nil, errors.New("该 VKey 有多个在线实例，请先排查重复启动；为避免升级错误设备，本次未下发")
			}
			selected = session
		}
	}
	if selected == nil {
		return nil, errors.New("客户端当前离线")
	}
	return selected, nil
}

func (s *Bridge) ClientUpdater(id int) (*common.UpdateCapability, string) {
	session, err := s.updateSession(id)
	if err != nil {
		return nil, err.Error()
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.Instance.Updater == nil {
		if session.Instance.OS == "android" {
			return &common.UpdateCapability{Protocol: 1, Kind: "android", Reason: "Android 请安装新版 APK，不支持二进制自替换"}, "Android 请安装新版 APK，不支持二进制自替换"
		}
		return &common.UpdateCapability{Protocol: 1, Kind: "legacy", Enabled: true}, "旧客户端不提供进度回报，可能需要先手动升级一次"
	}
	capability := *session.Instance.Updater
	return &capability, capability.Reason
}

func (s *Bridge) StartClientUpdate(id int, target string) (*ClientUpdateJob, error) {
	if !beego.AppConfig.DefaultBool("allow_remote_update", true) {
		return nil, errors.New("服务端已全局禁止远程升级")
	}
	if _, err := version.Compare(target, target); err != nil {
		return nil, err
	}
	session, err := s.updateSession(id)
	if err != nil {
		return nil, err
	}
	session.mu.Lock()
	signal, current, identity := session.signal, session.Version, session.Instance
	session.mu.Unlock()
	if cmp, err := version.Compare(current, target); err != nil || cmp >= 0 {
		return nil, errors.New("目标版本必须高于客户端当前版本")
	}
	legacy := identity.Updater == nil || identity.Updater.Protocol < 2
	if signal == nil || signal.Conn == nil {
		return nil, errors.New("客户端已断线")
	}
	if legacy && identity.OS == "android" {
		return nil, errors.New("Android 请安装新版 APK")
	}
	if !legacy && !identity.Updater.Enabled {
		return nil, fmt.Errorf("客户端不支持本次远程升级：%s", identity.Updater.Reason)
	}
	if legacy {
		if cmp, err := version.Compare(current, version.ClientUpdateMinVersion); err != nil || cmp < 0 {
			return nil, errors.New("该客户端需要先手动升级")
		}
	}
	token := make([]byte, 16)
	if _, err = rand.Read(token); err != nil {
		return nil, err
	}
	now := time.Now()
	job := &ClientUpdateJob{JobID: hex.EncodeToString(token), ClientID: id, InstanceID: identity.InstanceID, Current: current, Target: target, Stage: "sent", Message: "升级指令已发送，等待客户端接收", Legacy: legacy, CreatedAt: now, UpdatedAt: now}
	if legacy {
		job.Message = "旧客户端已下发；不支持进度回报，等待新版本重新上线"
	}
	s.updateMu.Lock()
	if s.updateJobs == nil {
		s.updateJobs = make(map[int]*ClientUpdateJob)
	}
	if previous := s.updateJobs[id]; previous != nil && !previous.Finished && time.Since(previous.CreatedAt) < 15*time.Minute {
		s.updateMu.Unlock()
		return nil, errors.New("此客户端已有升级任务，请勿重复下发")
	}
	// Keep at most one recent task per client, and expire idle entries.
	for key, old := range s.updateJobs {
		if time.Since(old.UpdatedAt) > 24*time.Hour {
			delete(s.updateJobs, key)
		}
	}
	s.updateJobs[id] = job
	s.updateMu.Unlock()
	session.sendMu.Lock()
	_ = signal.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if legacy {
		_, err = signal.Write([]byte(common.CLIENT_UPDATE))
	} else {
		_, err = signal.SendInfo(common.ClientUpdateRequest{JobID: job.JobID, Version: target}, common.CLIENT_UPDATE_REQUEST)
	}
	_ = signal.SetWriteDeadline(time.Time{})
	session.sendMu.Unlock()
	if err != nil {
		s.setUpdateProgress(id, common.ClientUpdateProgress{JobID: job.JobID, Stage: "failed", Message: "发送升级指令失败"})
		return nil, err
	}
	return s.ClientUpdateStatus(id), nil
}

func (s *Bridge) setUpdateProgress(id int, progress common.ClientUpdateProgress) {
	if s == nil {
		return
	}
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	job := s.updateJobs[id]
	if job == nil || progress.JobID != job.JobID || job.Finished {
		return
	}
	switch progress.Stage {
	case "accepted", "checking", "downloading", "verifying", "installing", "restarting", "failed", "no_update":
	default:
		return
	}
	job.Stage = progress.Stage
	job.Message = progress.Message
	if len(job.Message) > 2048 {
		job.Message = job.Message[:2048]
	}
	job.UpdatedAt = time.Now()
	job.Finished = updateStageTerminal(job.Stage)
	if job.Finished {
		recordClientUpdateResult(*job)
	}
}

func (s *Bridge) receiveUpdateProgress(id int, c *conn.Conn, info string) bool {
	if !strings.HasPrefix(info, common.ClientUpdateHealthPrefix) {
		return false
	}
	encoded := strings.TrimPrefix(info, common.ClientUpdateHealthPrefix)
	if len(encoded) > 8192 {
		return true
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return true
	}
	var progress common.ClientUpdateProgress
	if json.Unmarshal(data, &progress) != nil {
		return true
	}
	session, err := s.updateSession(id)
	if err != nil {
		return true
	}
	session.mu.Lock()
	current := session.signal
	session.mu.Unlock()
	if current == c {
		s.setUpdateProgress(id, progress)
	}
	return true
}

func (s *Bridge) ClientUpdateStatus(id int) *ClientUpdateJob {
	if s == nil {
		return nil
	}
	// A reconnect can briefly overlap the old signal's cleanup. Re-evaluate
	// readiness when queried so that overlap cannot leave a healthy update
	// stuck forever in "restarting".
	s.confirmClientUpdate(id)
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	job := s.updateJobs[id]
	if job == nil {
		return nil
	}
	if !job.Finished && time.Since(job.CreatedAt) > 15*time.Minute {
		job.Stage = "unconfirmed"
		job.Finished = true
		job.Message = "超时未确认目标版本上线，请查看客户端 npc-updater.log；未自动判定为升级成功"
		job.UpdatedAt = time.Now()
		recordClientUpdateResult(*job)
	}
	copy := *job
	return &copy
}

func (s *Bridge) confirmClientUpdate(id int) {
	session, err := s.updateSession(id)
	if err != nil {
		return
	}
	session.mu.Lock()
	release, dataVersion, identity := session.Version, session.DataVersion, session.Instance
	online := session.signal != nil && session.tunnel != nil && !session.tunnel.IsClose()
	session.mu.Unlock()
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	job := s.updateJobs[id]
	if job == nil || job.Finished {
		return
	}
	if !job.Legacy && identity.UpdateJobID != job.JobID {
		return
	}
	if identity.UpdateResult != nil && identity.UpdateResult.JobID == job.JobID && identity.UpdateResult.Stage == "failed" {
		job.Stage = "failed"
		job.Message = identity.UpdateResult.Message
		job.Finished = true
		job.UpdatedAt = time.Now()
		recordClientUpdateResult(*job)
		return
	}
	// MAIN alone does not mean traffic works: wait for the matching data mux.
	if !online {
		return
	}
	if cmp, err := version.Compare(dataVersion, job.Target); err != nil || cmp != 0 {
		return
	}
	cmp, err := version.Compare(release, job.Target)
	if err != nil || cmp != 0 {
		return
	}
	job.Stage = "completed"
	job.Message = "目标版本已重新上线，升级完成"
	job.Finished = true
	job.UpdatedAt = time.Now()
	recordClientUpdateResult(*job)
}

func recordClientUpdateResult(job ClientUpdateJob) {
	result := connectionlog.ResultFailure
	if job.Stage == "completed" || job.Stage == "no_update" {
		result = connectionlog.ResultSuccess
	}
	recordBridgeEvent(bridgeEventOptions{clientID: job.ClientID, action: connectionlog.ActionBridgeUpdate, result: result, channel: common.WORK_MAIN,
		stage: job.Stage, clientVersion: job.Target, message: fmt.Sprintf("升级任务 %s，%s → %s：%s", job.JobID, job.Current, job.Target, job.Message)})
}

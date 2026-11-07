package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/version"
)

type UpdateContext struct {
	common.ClientUpdateRequest
	VKey       string
	HTTPClient *http.Client
	Report     func(stage, version, message string)
}

var updaterMetadata struct {
	sync.RWMutex
	capability common.UpdateCapability
	handler    func(UpdateContext) (bool, error)
	result     *common.ClientUpdateProgress
}

// ConfigureRemoteUpdater also advertises unsupported/disabled executables.
// SDK/Fyne embedders that do not register an updater report that explicitly.
func ConfigureRemoteUpdater(capability common.UpdateCapability, handler func(UpdateContext) (bool, error)) {
	updaterMetadata.Lock()
	updaterMetadata.capability = capability
	updaterMetadata.handler = handler
	updaterMetadata.Unlock()
	SetRemoteUpdateHandler(func(vKey string) (bool, error) {
		if handler == nil {
			return false, fmt.Errorf("%s", capability.Reason)
		}
		return handler(UpdateContext{VKey: vKey, Report: func(string, string, string) {}})
	})
}

func SetUpdateResult(result *common.ClientUpdateProgress) {
	updaterMetadata.Lock()
	defer updaterMetadata.Unlock()
	if result == nil {
		updaterMetadata.result = nil
		return
	}
	copy := *result
	updaterMetadata.result = &copy
}

func (s *TRPClient) updateIdentity() (*common.UpdateCapability, *common.ClientUpdateProgress) {
	updaterMetadata.RLock()
	capability, result := updaterMetadata.capability, updaterMetadata.result
	if result != nil {
		copy := *result
		result = &copy
	}
	updaterMetadata.RUnlock()
	if capability.Protocol == 0 {
		capability = common.UpdateCapability{Protocol: 2, Kind: "embedded", Mode: "host", Reason: "当前嵌入端由宿主程序管理，请手动升级"}
	}
	if s.cnf != nil && s.cnf.CommonConfig != nil && s.cnf.CommonConfig.DisableRemoteUpdate {
		capability.Enabled = false
		capability.Reason = "客户端配置已禁止远程升级"
	}
	return &capability, result
}

// UpdateHTTPClient uses the same outbound proxy as this NPC connection,
// including SOCKS4/5 and HTTP CONNECT. Proxy credentials are never reported.
func UpdateHTTPClient(proxyURL, localIP string) *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{Transport: http.DefaultTransport, Timeout: 10 * time.Minute}
	}
	transport := base.Clone()
	if proxyURL != "" {
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialBridgeTCP(ctx, proxyURL, addr, localIP)
		}
	} else if localIP != "" || os.Getenv("ALL_PROXY") != "" || os.Getenv("all_proxy") != "" {
		// Match Bridge's ALL_PROXY preference, disabling Transport's separate
		// HTTP proxy layer so the same connection is not proxied twice.
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialBridgeTCP(ctx, "", addr, localIP)
		}
		if os.Getenv("ALL_PROXY") != "" || os.Getenv("all_proxy") != "" {
			transport.Proxy = nil
		}
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Minute}
}

func requestRemoteUpdateV2(s *TRPClient, signal *conn.Conn, request common.ClientUpdateRequest) {
	if request.JobID == "" || len(request.JobID) > 128 {
		return
	}
	if _, err := version.Compare(request.Version, request.Version); err != nil {
		return
	}
	report := func(stage, release, message string) {
		message = safeUpdateError(message, s.vKey, s.proxyUrl)
		if len(message) > 2048 {
			message = message[:2048]
		}
		payload, _ := json.Marshal(common.ClientUpdateProgress{JobID: request.JobID, Stage: stage, Version: release, Message: message})
		info := common.ClientUpdateHealthPrefix + base64.RawURLEncoding.EncodeToString(payload)
		frame, err := conn.GetLenBytes([]byte(info + common.CONN_DATA_SEQ + "true"))
		if err == nil {
			_, err = signal.WriteTimeout(frame, 3*time.Second)
		}
		if err != nil {
			s.logWarn("升级进度回报失败：%v", err)
		}
	}
	go func() {
		capability, _ := s.updateIdentity()
		if !capability.Enabled {
			report("failed", "", capability.Reason)
			return
		}
		if !remoteUpdateInProgress.CompareAndSwap(false, true) {
			report("failed", "", "本机已有升级任务执行中，请勿重复下发")
			return
		}
		keepLock := false
		defer func() {
			if r := recover(); r != nil {
				report("failed", "", "客户端升级器发生异常，旧连接保持运行")
			}
			if !keepLock {
				remoteUpdateInProgress.Store(false)
			}
		}()
		updaterMetadata.RLock()
		handler := updaterMetadata.handler
		updaterMetadata.RUnlock()
		if handler == nil {
			report("failed", "", "当前客户端没有注册升级器")
			return
		}
		report("accepted", request.Version, "客户端已接收升级任务")
		httpClient := UpdateHTTPClient(s.proxyUrl, s.localIP)
		defer httpClient.CloseIdleConnections()
		restarting, err := handler(UpdateContext{ClientUpdateRequest: request, VKey: s.vKey, HTTPClient: httpClient, Report: report})
		if err != nil {
			report("failed", "", err.Error())
			s.logError("[更新异常] %s", safeUpdateError(err.Error(), s.vKey, s.proxyUrl))
			return
		}
		if !restarting {
			report("no_update", request.Version, "当前运行版本无需更新")
			return
		}
		keepLock = true
	}()
}

func safeUpdateError(message, vKey, proxyURL string) string {
	if vKey != "" {
		message = strings.ReplaceAll(message, vKey, "[redacted]")
	}
	if proxyURL != "" {
		message = strings.ReplaceAll(message, proxyURL, "[outbound proxy]")
	}
	// net/url errors can include authentication from a malformed proxy URL.
	if parsed, err := url.Parse(proxyURL); err == nil && parsed.User != nil {
		if password, ok := parsed.User.Password(); ok && password != "" {
			message = strings.ReplaceAll(message, password, "[redacted]")
		}
	}
	return message
}

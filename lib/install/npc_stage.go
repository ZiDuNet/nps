package install

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ehang.io/nps/lib/version"
)

type UpdateReporter func(stage, version, message string)

// NpcUpdatePackage is fully downloaded and verified before any running
// connection is stopped. Cleanup only removes the private staging directory.
type NpcUpdatePackage struct {
	Directory string `json:"directory"`
	Binary    string `json:"binary"`
	Version   string `json:"version"`
}

func (p *NpcUpdatePackage) Cleanup() {
	if p != nil && p.Directory != "" {
		_ = os.RemoveAll(p.Directory)
	}
}

func PrepareNpcUpdate(targetVersion string, force bool, client *http.Client, report UpdateReporter) (*NpcUpdatePackage, error) {
	if report == nil {
		report = func(string, string, string) {}
	}
	report("checking", targetVersion, "正在获取正式版本信息")
	rl, err := fetchRelease(targetVersion, client)
	if err != nil {
		return nil, fmt.Errorf("获取版本信息失败：%w", err)
	}
	comparison, err := version.Compare(version.VERSION, rl.TagName)
	if err != nil {
		return nil, err
	}
	if comparison >= 0 && !force {
		return nil, nil
	}
	dir, err := CreatePrivateUpdateDirectory("", "nps-npc-update-*")
	if err != nil {
		return nil, fmt.Errorf("创建更新临时目录失败：%w", err)
	}
	p := &NpcUpdatePackage{Directory: dir, Version: rl.TagName}
	ok := false
	defer func() {
		if !ok {
			p.Cleanup()
		}
	}()
	root, err := downloadReleaseWithClient(rl, "client", dir, client, report)
	if err != nil {
		return nil, err
	}
	p.Binary, err = packageBinaryPath(root, "npc", false)
	if err != nil {
		p.Binary, err = packageBinaryPath(root, "npc", true)
	}
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(p.Binary, 0755); err != nil {
		return nil, err
	}
	report("verifying", p.Version, "校验新程序版本与可运行性")
	if err = VerifyNpcBinary(p.Binary, p.Version); err != nil {
		return nil, err
	}
	ok = true
	return p, nil
}

func VerifyNpcBinary(path, expected string) error {
	release, err := NpcBinaryVersion(path)
	if err != nil {
		return err
	}
	cmp, err := version.Compare(release, expected)
	if err == nil && cmp == 0 {
		return nil
	}
	return fmt.Errorf("新程序版本与预期 %s 不符", expected)
}

func NpcBinaryVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("新程序启动自检失败：%w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Version: ") {
			release := strings.TrimSpace(strings.TrimPrefix(line, "Version: "))
			if _, err := version.Compare(release, release); err == nil {
				return release, nil
			}
		}
	}
	return "", fmt.Errorf("无法识别 NPC 程序版本")
}

// BinaryTransaction retains the original until startup succeeds. Backups
// live beside the actual executable, including renamed/legacy service paths.
type BinaryTransaction struct{ Target, Backup string }

func InstallBinary(source, target string) (*BinaryTransaction, error) {
	target = stableExecutablePath(target)
	if err := PreflightUpdateTarget(target, ""); err != nil {
		return nil, err
	}
	permissions, err := captureExecutablePermissions(target)
	if err != nil {
		return nil, err
	}
	probe, err := os.CreateTemp(filepath.Dir(target), ".npc-rollback-*")
	if err != nil {
		return nil, fmt.Errorf("安装目录不可写：%w", err)
	}
	backup := probe.Name()
	_ = probe.Close()
	tx := &BinaryTransaction{Target: target, Backup: backup}
	if err := copyFileAtomicConfigured(target, backup, permissions); err != nil {
		_ = os.Remove(backup)
		return nil, err
	}
	if err := replaceExecutable(source, target); err != nil {
		_ = os.Remove(backup)
		return nil, err
	}
	return tx, nil
}

func (tx *BinaryTransaction) Rollback() error {
	if tx == nil {
		return nil
	}
	if err := replaceExecutable(tx.Backup, tx.Target); err != nil {
		return err
	}
	tx.Commit()
	return nil
}

func (tx *BinaryTransaction) Commit() {
	if tx != nil {
		_ = os.Remove(tx.Backup)
	}
}

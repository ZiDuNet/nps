//go:build !windows

package install

import (
	"bufio"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// RegisteredServiceExecutable reads the registered path, not a guessed
// /usr/bin copy. We deliberately reject wrappers/ambiguous configurations.
func RegisteredServiceExecutable(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "/\\\r\n") {
		return "", errors.New("无效的服务名称")
	}
	if runtime.GOOS == "darwin" {
		paths := []string{filepath.Join("/Library/LaunchDaemons", name+".plist")}
		if dir, err := os.UserHomeDir(); err == nil {
			paths = append(paths, filepath.Join(dir, "Library/LaunchAgents", name+".plist"))
		}
		for _, path := range paths {
			if data, err := os.ReadFile(path); err == nil {
				return plistExecutable(data)
			}
		}
		return "", fmt.Errorf("未找到服务 %s 的 plist", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if data, err := exec.CommandContext(ctx, "systemctl", "show", name, "--property=ExecStart", "--value").Output(); err == nil {
		if path, err := systemdExecutable(string(data)); err == nil {
			return path, nil
		}
	}
	data, err := os.ReadFile(filepath.Join("/etc/init.d", name))
	if err != nil {
		return "", fmt.Errorf("无法读取服务 %s 的程序路径：%w", name, err)
	}
	return sysvExecutable(string(data))
}

func systemdExecutable(value string) (string, error) {
	index := strings.Index(value, "path=")
	if index >= 0 {
		path, _, ok := strings.Cut(value[index+5:], " ;")
		if ok && filepath.IsAbs(strings.TrimSpace(path)) {
			return strings.TrimSpace(path), nil
		}
	}
	return "", errors.New("服务启动路径不是可识别的直接可执行文件")
}

func sysvExecutable(value string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(value))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		for _, prefix := range []string{"cmd=", "command="} {
			if strings.HasPrefix(line, prefix) {
				line = strings.TrimPrefix(line, prefix)
				line = strings.TrimLeft(line, "\"'")
				if end := strings.IndexAny(line, " \t\"'"); end > 0 {
					line = line[:end]
				}
				if filepath.IsAbs(line) && !strings.ContainsAny(line, "$`;") {
					return line, nil
				}
			}
		}
	}
	return "", errors.New("无法安全识别 SysV 服务路径，请检查启动脚本")
}

func plistExecutable(data []byte) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	key := ""
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local == "key" {
			_ = decoder.DecodeElement(&key, &start)
		}
		if start.Name.Local == "string" && (key == "Program" || key == "ProgramArguments") {
			var path string
			if decoder.DecodeElement(&path, &start) == nil && filepath.IsAbs(path) {
				return path, nil
			}
		}
	}
	return "", errors.New("plist 中没有直接程序路径")
}

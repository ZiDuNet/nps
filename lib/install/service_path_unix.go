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
	executable, _, err := registeredServiceCommand(name)
	return executable, err
}

type ServiceCommand struct {
	Executable  string
	Arguments   []string
	Environment []string
	ConfigPath  string
}

// RegisteredServiceCommand reads the direct executable, its arguments and
// its systemd Environment= values. Menu health checks use the service's real
// -conf_path instead of the menu process's current directory.
func RegisteredServiceCommand(name string) (ServiceCommand, error) {
	if name == "" || strings.ContainsAny(name, "/\\\r\n") {
		return ServiceCommand{}, errors.New("无效的服务名称")
	}
	if runtime.GOOS == "darwin" {
		paths := []string{filepath.Join("/Library/LaunchDaemons", name+".plist")}
		if dir, err := os.UserHomeDir(); err == nil {
			paths = append(paths, filepath.Join(dir, "Library/LaunchAgents", name+".plist"))
		}
		for _, path := range paths {
			if data, err := os.ReadFile(path); err == nil {
				executable, args, err := plistCommand(data)
				if err != nil {
					return ServiceCommand{}, err
				}
				return commandWithConfig(ServiceCommand{Executable: executable, Arguments: args}), nil
			}
		}
		return ServiceCommand{}, fmt.Errorf("未找到服务 %s 的 plist", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if data, err := exec.CommandContext(ctx, "systemctl", "show", name,
		"--property=ExecStart", "--property=Environment").Output(); err == nil {
		command, parseErr := parseSystemdShowOutput(string(data))
		if parseErr == nil {
			return command, nil
		}
	}
	data, err := os.ReadFile(filepath.Join("/etc/init.d", name))
	if err != nil {
		return ServiceCommand{}, fmt.Errorf("无法读取服务 %s 的启动配置：%w", name, err)
	}
	executable, args, err := sysvCommand(string(data))
	if err != nil {
		return ServiceCommand{}, err
	}
	return ServiceCommand{Executable: executable, Arguments: args}, nil
}

func registeredServiceCommand(name string) (string, []string, error) {
	if name == "" || strings.ContainsAny(name, "/\\\r\n") {
		return "", nil, errors.New("无效的服务名称")
	}
	if runtime.GOOS == "darwin" {
		paths := []string{filepath.Join("/Library/LaunchDaemons", name+".plist")}
		if dir, err := os.UserHomeDir(); err == nil {
			paths = append(paths, filepath.Join(dir, "Library/LaunchAgents", name+".plist"))
		}
		for _, path := range paths {
			if data, err := os.ReadFile(path); err == nil {
				return plistCommand(data)
			}
		}
		return "", nil, fmt.Errorf("未找到服务 %s 的 plist", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if data, err := exec.CommandContext(ctx, "systemctl", "show", name, "--property=ExecStart", "--value").Output(); err == nil {
		if command, err := parseSystemdCommand(string(data)); err == nil {
			return command.Executable, command.Arguments, nil
		}
	}
	data, err := os.ReadFile(filepath.Join("/etc/init.d", name))
	if err != nil {
		return "", nil, fmt.Errorf("无法读取服务 %s 的程序路径：%w", name, err)
	}
	return sysvCommand(string(data))
}

func systemdExecutable(value string) (string, error) {
	command, err := parseSystemdCommand(value)
	if err != nil {
		return "", err
	}
	return command.Executable, nil
}

func parseSystemdCommand(value string) (ServiceCommand, error) {
	var command ServiceCommand
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Environment=") {
			command.Environment = append(command.Environment, parseSystemdEnvironment(strings.TrimPrefix(line, "Environment="))...)
			continue
		}
		if !strings.Contains(line, "path=") {
			continue
		}
		executable, args, err := parseSystemdExecStart(line)
		if err != nil {
			return ServiceCommand{}, err
		}
		command.Executable, command.Arguments = executable, args
		command = commandWithConfig(command)
	}
	if command.Executable == "" {
		return ServiceCommand{}, errors.New("服务启动路径不是可识别的直接可执行文件")
	}
	return command, nil
}

// systemctl show --property=... renders one named property per line. Do not
// use --value with multiple properties: ExecStart and Environment then become
// unnamed lines, and a valid Environment=[] helpfully collapses to an empty
// value. Keeping the names also lets us tolerate old/new systemd output.
func parseSystemdShowOutput(value string) (ServiceCommand, error) {
	var execStart strings.Builder
	var environment []string
	for _, raw := range strings.Split(value, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "ExecStart="):
			if execStart.Len() > 0 {
				execStart.WriteByte('\n')
			}
			execStart.WriteString(strings.TrimPrefix(line, "ExecStart="))
		case strings.HasPrefix(line, "Environment="):
			environment = append(environment,
				parseSystemdEnvironment(strings.TrimPrefix(line, "Environment="))...)
		case strings.Contains(line, "path="):
			// Be liberal with callers/tests that pass only the ExecStart value.
			if execStart.Len() > 0 {
				execStart.WriteByte('\n')
			}
			execStart.WriteString(line)
		}
	}
	if execStart.Len() == 0 {
		return ServiceCommand{}, errors.New("服务启动路径不是可识别的直接可执行文件")
	}
	command, err := parseSystemdCommand(execStart.String())
	if err != nil {
		return ServiceCommand{}, err
	}
	command.Environment = append(command.Environment, environment...)
	return command, nil
}

func parseSystemdExecStart(value string) (string, []string, error) {
	index := strings.Index(value, "path=")
	if index < 0 {
		return "", nil, errors.New("systemd ExecStart 缺少 path")
	}
	pathValue := strings.TrimSpace(value[index+len("path="):])
	if end := strings.Index(pathValue, ";"); end >= 0 {
		pathValue = strings.TrimSpace(pathValue[:end])
	}
	fields := splitSystemdCommandPreservingPath(pathValue)
	if len(fields) == 0 || !filepath.IsAbs(fields[0]) {
		return "", nil, errors.New("systemd ExecStart 程序路径不是绝对路径")
	}
	executable := fields[0]
	var arguments []string
	argvIndex := strings.Index(value, "argv[]=")
	if argvIndex >= 0 {
		argv := strings.TrimSpace(value[argvIndex+len("argv[]="):])
		argvFields := splitSystemdArgv(argv)
		if len(argvFields) > 0 {
			if argvFields[0] == executable {
				argvFields = argvFields[1:]
			}
			arguments = argvFields
		}
	}
	return executable, arguments, nil
}

// systemd renders ExecStart's argv field with spaces inside an executable
// path unquoted. Match the longest valid argv prefix against the path field,
// then treat the remaining tokens as arguments.
func splitSystemdArgv(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if end := strings.Index(value, ";"); end >= 0 {
		value = strings.TrimSpace(value[:end])
	}
	fields := splitSystemdCommand(value)
	return fields
}

// systemd's argv field is authoritative. Path= is only parsed when argv is
// unavailable, and it can contain spaces without quoting, so the executable
// is the whole field rather than its first token.
func splitSystemdCommandPreservingPath(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if !strings.ContainsAny(value, "\"'") {
		return []string{value}
	}
	return splitSystemdCommand(value)
}

func splitSystemdCommand(value string) []string {
	var (
		fields  []string
		current strings.Builder
		quote   rune
		escaped bool
	)
	flush := func() {
		if current.Len() > 0 {
			fields = append(fields, current.String())
		}
		current.Reset()
	}
	lastSpace := true
	for _, char := range value {
		if escaped {
			current.WriteRune(char)
			escaped = false
			lastSpace = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				current.WriteRune(char)
			}
			lastSpace = false
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			lastSpace = false
			continue
		}
		if char == ';' {
			flush()
			break
		}
		if char == ' ' || char == '\t' {
			if !lastSpace {
				flush()
			}
			lastSpace = true
			continue
		}
		current.WriteRune(char)
		lastSpace = false
	}
	if !lastSpace {
		flush()
	}
	if len(fields) == 1 && fields[0] == value {
		fields = strings.Fields(value)
	}
	return fields
}

func parseSystemdEnvironment(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || value == "[]" {
		return nil
	}
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	fields := splitSystemdCommand(value)
	if len(fields) == 1 && strings.Contains(fields[0], " ") {
		fields = strings.Fields(fields[0])
	}
	return fields
}

func commandWithConfig(command ServiceCommand) ServiceCommand {
	for _, argument := range command.Arguments {
		for _, prefix := range []string{"-conf_path", "--conf_path"} {
			if argument == prefix {
				continue
			}
			if strings.HasPrefix(argument, prefix+"=") {
				command.ConfigPath = strings.TrimSpace(strings.TrimPrefix(argument, prefix+"="))
				return command
			}
		}
	}
	for index, argument := range command.Arguments {
		if (argument == "-conf_path" || argument == "--conf_path") && index+1 < len(command.Arguments) {
			command.ConfigPath = strings.TrimSpace(command.Arguments[index+1])
			return command
		}
	}
	return command
}

func sysvCommand(value string) (string, []string, error) {
	scanner := bufio.NewScanner(strings.NewReader(value))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		for _, prefix := range []string{"cmd=", "command="} {
			if strings.HasPrefix(line, prefix) {
				commandText := strings.TrimPrefix(line, prefix)
				commandText = strings.Trim(commandText, "\"'")
				fields := strings.Fields(commandText)
				if len(fields) == 0 || !filepath.IsAbs(fields[0]) || strings.ContainsAny(fields[0], "$`;") {
					continue
				}
				return fields[0], fields[1:], nil
			}
		}
	}
	return "", nil, errors.New("无法安全识别 SysV 服务路径，请检查启动脚本")
}

func sysvExecutable(value string) (string, error) {
	executable, _, err := sysvCommand(value)
	return executable, err
}

func plistExecutable(data []byte) (string, error) {
	executable, _, err := plistCommand(data)
	return executable, err
}

func plistCommand(data []byte) (string, []string, error) {
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
				return path, nil, nil
			}
		}
	}
	return "", nil, errors.New("plist 中没有直接程序路径")
}

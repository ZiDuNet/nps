//go:build windows

package install

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

type ServiceCommand struct {
	Executable  string
	Arguments   []string
	Environment []string
	ConfigPath  string
}

// RegisteredServiceCommand reads the service's complete command line so menu
// upgrades can retain its real -conf_path and later health-check the same data
// directory. Windows service environment blocks remain owned by the SCM.
func RegisteredServiceCommand(name string) (ServiceCommand, error) {
	if strings.TrimSpace(name) == "" {
		return ServiceCommand{}, fmt.Errorf("服务名称为空")
	}
	m, err := connectUpdateSCM(windows.SC_MANAGER_CONNECT)
	if err != nil {
		return ServiceCommand{}, err
	}
	defer m.Disconnect()
	s, err := openUpdateService(m, name, windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return ServiceCommand{}, err
	}
	defer s.Close()
	config, err := s.Config()
	if err != nil {
		return ServiceCommand{}, err
	}
	args, err := windows.DecomposeCommandLine(config.BinaryPathName)
	if err != nil || len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return ServiceCommand{}, fmt.Errorf("服务 %s 程序路径无法解析", name)
	}
	return commandWithConfig(ServiceCommand{
		Executable: strings.TrimSpace(args[0]),
		Arguments:  append([]string(nil), args[1:]...),
	}), nil
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

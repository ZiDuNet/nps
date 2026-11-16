//go:build !linux && !sdk

package main

import (
	"errors"
	"os/exec"
)

func lockFNOSConfig(string) (func(), error) {
	return nil, errors.New("飞牛配置只支持 Linux")
}

func configureFNOSProcess(*exec.Cmd) {}

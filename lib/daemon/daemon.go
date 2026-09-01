package daemon

import (
	"errors"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"ehang.io/nps/lib/common"
)

func InitDaemon(f string, runPath string, pidPath string) {
	if len(os.Args) < 2 {
		return
	}
	var args []string
	args = append(args, os.Args[0])
	if len(os.Args) >= 2 {
		args = append(args, os.Args[2:]...)
	}
	args = append(args, "-log=file")
	switch os.Args[1] {
	case "start":
		start(args, f, pidPath, runPath)
		os.Exit(0)
	case "stop":
		stop(f, args[0], pidPath)
		os.Exit(0)
	case "restart":
		stop(f, args[0], pidPath)
		start(args, f, pidPath, runPath)
		os.Exit(0)
	case "status":
		if status(f, pidPath) {
			log.Printf("%s is running", f)
		} else {
			log.Printf("%s is not running", f)
		}
		os.Exit(0)
	case "reload":
		reload(f, pidPath)
		os.Exit(0)
	}
}

func reload(f string, pidPath string) {
	if common.IsWindows() {
		log.Println("reload is not supported on Windows; use restart")
		return
	}
	if f == "nps" && !common.IsWindows() && !status(f, pidPath) {
		log.Println("reload fail")
		return
	}
	pid, err := readPID(pidPath, f)
	if err != nil {
		log.Println("reload error:", err)
		return
	}
	// Pass the PID as an argument instead of interpolating it into a shell
	// command. This prevents a tampered pid file from becoming command
	// injection while retaining compatibility with Unix environments.
	c := exec.Command("kill", "-30", pid)
	if c.Run() == nil {
		log.Println("reload success")
	} else {
		log.Println("reload fail")
	}
}

func status(f string, pidPath string) bool {
	pid, err := readPID(pidPath, f)
	if err != nil {
		return false
	}
	if common.IsWindows() {
		out, err := exec.Command("tasklist", "/FI", "PID eq "+pid).Output()
		return err == nil && strings.Contains(string(out), pid)
	}
	out, err := exec.Command("ps", "-p", pid, "-o", "pid=").Output()
	return err == nil && strings.TrimSpace(string(out)) == pid
}

func start(osArgs []string, f string, pidPath, runPath string) {
	if status(f, pidPath) {
		log.Printf(" %s is running", f)
		return
	}
	cmd := exec.Command(osArgs[0], osArgs[1:]...)
	if err := cmd.Start(); err != nil {
		log.Println("start error:", err)
		return
	}
	if cmd.Process != nil && cmd.Process.Pid > 0 {
		log.Println("start ok , pid:", cmd.Process.Pid, "config path:", runPath)
		d1 := []byte(strconv.Itoa(cmd.Process.Pid))
		if err := ioutil.WriteFile(filepath.Join(pidPath, f+".pid"), d1, 0600); err != nil {
			log.Println("write pid file error:", err)
		}
	} else {
		log.Println("start error")
	}
}

func stop(f string, p string, pidPath string) {
	if !status(f, pidPath) {
		log.Printf(" %s is not running", f)
		return
	}
	var c *exec.Cmd
	if common.IsWindows() {
		p := strings.Split(p, `\`)
		c = exec.Command("taskkill", "/F", "/IM", p[len(p)-1])
	} else {
		pid, err := readPID(pidPath, f)
		if err != nil {
			log.Println("stop error:", err)
			return
		}
		c = exec.Command("kill", "-9", pid)
	}
	err := c.Run()
	if err != nil {
		log.Println("stop error,", err)
	} else {
		log.Println("stop ok")
	}
}

func readPID(pidPath, name string) (string, error) {
	b, err := ioutil.ReadFile(filepath.Join(pidPath, name+".pid"))
	if err != nil {
		return "", err
	}
	pid := strings.TrimSpace(string(b))
	n, err := strconv.Atoi(pid)
	if err != nil || n <= 0 {
		return "", errors.New("invalid pid file")
	}
	return strconv.Itoa(n), nil
}

//go:build linux || android

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func maybeDaemonize(enabled bool) {
	if !enabled || os.Getenv("ADBLINK_DAEMON_CHILD") == "1" {
		return
	}

	cmd := exec.Command(os.Args[0], os.Args[1:]...)
	cmd.Env = append(os.Environ(), "ADBLINK_DAEMON_CHILD=1")
	cmd.Stdin = nil

	logFile, err := os.OpenFile("/data/local/tmp/adblink.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	} else {
		cmd.Stdout = nil
		cmd.Stderr = nil
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start daemon: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("AdbLink Agent running in background (PID: %d)\n", cmd.Process.Pid)
	os.Exit(0)
}

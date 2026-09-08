package agent

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"time"
)

// EnsureAdbd checks if adbd is listening on targetAddr.
// If not reachable and autoAdbd is true, it attempts to enable adbd TCP port via setprop and service restart.
func EnsureAdbd(targetAddr string, autoAdbd bool, logger *log.Logger) error {
	// First test if it is already listening
	conn, err := net.DialTimeout("tcp", targetAddr, 1*time.Second)
	if err == nil {
		_ = conn.Close()
		return nil
	}

	if !autoAdbd {
		return fmt.Errorf("local adbd not responding at %s (%w). (Enable with -auto-adbd or run 'adb tcpip 5555')", targetAddr, err)
	}

	logger.Printf("Local adbd at %s not responding, attempting to enable TCP mode...", targetAddr)

	// Attempt to setprop service.adb.tcp.port 5555
	_, portStr, splitErr := net.SplitHostPort(targetAddr)
	if splitErr != nil {
		portStr = "5555"
	}

	// Run setprop
	_ = exec.Command("/system/bin/setprop", "service.adb.tcp.port", portStr).Run()
	_ = exec.Command("setprop", "service.adb.tcp.port", portStr).Run()

	// Restart adbd
	_ = exec.Command("/system/bin/setprop", "ctl.restart", "adbd").Run()
	_ = exec.Command("setprop", "ctl.restart", "adbd").Run()

	// Wait up to 3 seconds for adbd to restart and listen
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		testConn, testErr := net.DialTimeout("tcp", targetAddr, 500*time.Millisecond)
		if testErr == nil {
			_ = testConn.Close()
			logger.Printf("Successfully enabled adbd on %s", targetAddr)
			return nil
		}
	}

	return fmt.Errorf("failed to reach adbd at %s after attempting restart", targetAddr)
}

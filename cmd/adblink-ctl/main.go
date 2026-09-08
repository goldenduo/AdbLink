package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/goldenduo/AdbLink/pkg/server"
)

var version = "1.0.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	subcmd := os.Args[1]

	switch subcmd {
	case "list":
		cmdList(os.Args[2:])
	case "connect":
		cmdConnect(os.Args[2:])
	case "push":
		cmdPush(os.Args[2:])
	case "version":
		fmt.Printf("adblink-ctl v%s\n", version)
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n\n", subcmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`AdbLink CLI Control Utility

Usage:
  adblink-ctl <command> [arguments]

Commands:
  list                 List all connected devices from AdbLink server
  connect [device-id]  Connect host adb to an AdbLink device
  push                 Deploy and launch adblink-agent onto an ADB-connected device
  version              Show version information

Use "adblink-ctl <command> -h" for more information about a command.`)
}

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	webAddr := fs.String("server", "http://127.0.0.1:9001", "AdbLink Web API URL")
	_ = fs.Parse(args)

	url := strings.TrimRight(*webAddr, "/") + "/api/v1/devices"
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to query server %s: %v\n", url, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Server returned HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	var devices []server.DeviceInfo
	if err := json.NewDecoder(resp.Body).Decode(&devices); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse response: %v\n", err)
		os.Exit(1)
	}

	if len(devices) == 0 {
		fmt.Println("No devices currently connected.")
		return
	}

	fmt.Printf("%-18s %-16s %-10s %-8s %-12s %-24s\n",
		"DEVICE ID", "MODEL", "STATUS", "PORT", "STREAMS", "CONNECT COMMAND")
	fmt.Println(strings.Repeat("-", 90))

	for _, d := range devices {
		fmt.Printf("%-18s %-16s %-10s %-8d %-12d adb connect %s\n",
			truncate(d.DeviceID, 17),
			truncate(d.Model, 15),
			d.Status,
			d.AssignedPort,
			d.ActiveStreams,
			d.AdbConnectTarget,
		)
	}
}

func cmdConnect(args []string) {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	webAddr := fs.String("server", "http://127.0.0.1:9001", "AdbLink Web API URL")
	_ = fs.Parse(args)

	targetID := ""
	if fs.NArg() > 0 {
		targetID = fs.Arg(0)
	}

	url := strings.TrimRight(*webAddr, "/") + "/api/v1/devices"
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to query server %s: %v\n", url, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var devices []server.DeviceInfo
	if err := json.NewDecoder(resp.Body).Decode(&devices); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse response: %v\n", err)
		os.Exit(1)
	}

	var selected *server.DeviceInfo
	for i := range devices {
		d := &devices[i]
		if targetID == "" || d.DeviceID == targetID || strings.Contains(d.DeviceID, targetID) {
			if d.Status == "ONLINE" {
				selected = d
				break
			}
		}
	}

	if selected == nil {
		if targetID == "" {
			fmt.Fprintln(os.Stderr, "No online devices found on server.")
		} else {
			fmt.Fprintf(os.Stderr, "Device '%s' not found or offline.\n", targetID)
		}
		os.Exit(1)
	}

	fmt.Printf("Connecting to %s (%s) at %s...\n", selected.DeviceID, selected.Model, selected.AdbConnectTarget)
	cmd := exec.Command("adb", "connect", selected.AdbConnectTarget)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "adb connect failed: %v\n", err)
		os.Exit(1)
	}
}

func cmdPush(args []string) {
	fs := flag.NewFlagSet("push", flag.ExitOnError)
	adbSerial := fs.String("s", "", "Target ADB device serial (e.g. 127.0.0.1:5558)")
	serverAddr := fs.String("server", "172.17.0.1:9000", "AdbLink server address to connect back to")
	agentBin := fs.String("bin", "", "Path to adblink-agent binary (auto-detects if empty)")
	token := fs.String("token", "", "Authentication token")
	_ = fs.Parse(args)

	// Detect device abi
	cmdArgs := []string{}
	if *adbSerial != "" {
		cmdArgs = append(cmdArgs, "-s", *adbSerial)
	}
	cmdArgs = append(cmdArgs, "shell", "getprop", "ro.product.cpu.abi")

	out, err := exec.Command("adb", cmdArgs...).Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to query target device: %v\n", err)
		os.Exit(1)
	}
	abi := strings.TrimSpace(string(out))
	fmt.Printf("Target device ABI: %s\n", abi)

	// Determine binary path
	binPath := *agentBin
	if binPath == "" {
		candidate := "bin/adblink-agent-android-arm64"
		if strings.Contains(abi, "x86_64") {
			candidate = "bin/adblink-agent-android-amd64"
		} else if strings.Contains(abi, "x86") {
			candidate = "bin/adblink-agent-android-386"
		} else if strings.Contains(abi, "arm") && !strings.Contains(abi, "64") {
			candidate = "bin/adblink-agent-android-arm"
		}

		if _, statErr := os.Stat(candidate); statErr == nil {
			binPath = candidate
		} else if _, statErr := os.Stat("bin/adblink-agent"); statErr == nil {
			binPath = "bin/adblink-agent"
		} else {
			fmt.Printf("Agent binary not found at %s. Building now...\n", candidate)
			buildCmd := exec.Command("go", "build", "-o", candidate, "./cmd/adblink-agent")
			buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64")
			if strings.Contains(abi, "x86_64") {
				buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
			}
			if bOut, bErr := buildCmd.CombinedOutput(); bErr != nil {
				fmt.Fprintf(os.Stderr, "Build failed: %s\n", string(bOut))
				os.Exit(1)
			}
			binPath = candidate
		}
	}

	remotePath := "/data/local/tmp/adblink-agent"
	fmt.Printf("Pushing %s to device %s:%s...\n", binPath, *adbSerial, remotePath)

	pushArgs := []string{}
	if *adbSerial != "" {
		pushArgs = append(pushArgs, "-s", *adbSerial)
	}
	pushArgs = append(pushArgs, "push", binPath, remotePath)
	if out, err := exec.Command("adb", pushArgs...).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "adb push failed: %s\n", string(out))
		os.Exit(1)
	}

	chmodArgs := []string{}
	if *adbSerial != "" {
		chmodArgs = append(chmodArgs, "-s", *adbSerial)
	}
	chmodArgs = append(chmodArgs, "shell", "chmod", "+x", remotePath)
	_ = exec.Command("adb", chmodArgs...).Run()

	fmt.Println("Launching agent on device...")
	runArgs := []string{}
	if *adbSerial != "" {
		runArgs = append(runArgs, "-s", *adbSerial)
	}
	runArgs = append(runArgs, "shell", remotePath, "-server", *serverAddr, "-d")
	if *token != "" {
		runArgs = append(runArgs, "-token", *token)
	}

	out, err = exec.Command("adb", runArgs...).CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start agent: %s\n", string(out))
		os.Exit(1)
	}
	if len(out) > 0 {
		fmt.Print(string(out))
	}

	fmt.Println("Agent deployed and running! Check logs with:")
	fmt.Printf("  adb %s shell cat /data/local/tmp/adblink.log\n", func() string {
		if *adbSerial != "" {
			return "-s " + *adbSerial
		}
		return ""
	}())
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

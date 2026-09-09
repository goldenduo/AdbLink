package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/goldenduo/AdbLink/pkg/server"
)

var version = "1.3.0"

func main() {
	if len(os.Args) < 2 {
		cmdAuto(nil)
		return
	}

	subcmd := os.Args[1]

	switch subcmd {
	case "list":
		cmdList(os.Args[2:])
	case "connect":
		cmdConnect(os.Args[2:])
	case "push":
		cmdPush(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Printf("adblink-ctl v%s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		// Run auto deploy & connect if not a subcommand
		cmdAuto(os.Args[1:])
	}
}

func printUsage() {
	fmt.Println(`AdbLink CLI Control Utility

Usage:
  adblink-ctl [server_address] [flags]
  adblink-ctl <command> [arguments]

Quick Start (Zero-Config Auto Deploy & Connect):
  Simply plug in your phone via USB and run:
    adblink-ctl
  Or connect to a specific remote server:
    adblink-ctl 1.2.3.4:8888

  The command will automatically:
    1. Detect your device and ABI (arm64, x86_64, arm)
    2. Enable TCP mode (adb tcpip 5555) for physical devices
    3. Push and run the static agent daemon in background
    4. Automatically connect your host adb to the exposed tunnel

Commands:
  list                 List all connected devices from AdbLink server
  connect [device-id]  Connect host adb to an AdbLink device
  push                 Deploy and launch adblink-agent onto an ADB-connected device
  version              Show version information

Flags (for default auto mode):
  -server string       Remote AdbLink server address (default: 127.0.0.1:8888 or $ADBLINK_SERVER)
  -s string            Target ADB device serial (auto-detected if omitted)
  -bin string          Custom agent binary path (auto-detected if omitted)
  -token string        Authentication token if required by server
  -web string          Server Web API URL (e.g. http://1.2.3.4:9999)`)
}

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	webAddr := fs.String("server", "http://127.0.0.1:9999", "AdbLink Web API URL")
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
	webAddr := fs.String("server", "http://127.0.0.1:9999", "AdbLink Web API URL")
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
	serverAddr := fs.String("server", "172.17.0.1:8888", "AdbLink server address to connect back to")
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
		binPath = findAgentBinary(abi)
		if binPath == "" {
			// Try auto-building if Go compiler is available
			candidate := filepath.Join("bin", "android", "adblink-agent-arm64")
			goArch := "arm64"
			if strings.Contains(abi, "x86_64") {
				candidate = filepath.Join("bin", "android", "adblink-agent-x86_64")
				goArch = "amd64"
			} else if strings.Contains(abi, "x86") {
				candidate = filepath.Join("bin", "android", "adblink-agent-x86")
				goArch = "386"
			} else if strings.Contains(abi, "arm") && !strings.Contains(abi, "64") {
				candidate = filepath.Join("bin", "android", "adblink-agent-armv7")
				goArch = "arm"
			}

			if _, lookErr := exec.LookPath("go"); lookErr == nil {
				fmt.Printf("Agent binary not found locally, compiling %s using Go...\n", candidate)
				_ = os.MkdirAll(filepath.Dir(candidate), 0755)
				buildCmd := exec.Command("go", "build", "-o", candidate, "./cmd/adblink-agent")
				buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goArch)
				if bOut, bErr := buildCmd.CombinedOutput(); bErr == nil {
					binPath = candidate
				} else {
					fmt.Fprintf(os.Stderr, "Auto-build failed: %s\n", string(bOut))
				}
			}

			if binPath == "" {
				fmt.Fprintf(os.Stderr, "Error: adblink-agent binary for architecture '%s' not found.\n", abi)
				fmt.Fprintf(os.Stderr, "Please download the agent binary or specify its location with -bin <path>.\n")
				os.Exit(1)
			}
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

	// Ensure device adbd is running in TCP mode on port 5555 (critical for non-root real Android phones connected via USB)
	if isUSBDevice(*adbSerial) {
		fmt.Println("Ensuring device adbd is in TCP mode (adb tcpip 5555)...")
		tcpipArgs := []string{}
		if *adbSerial != "" {
			tcpipArgs = append(tcpipArgs, "-s", *adbSerial)
		}
		tcpipArgs = append(tcpipArgs, "tcpip", "5555")
		if tcpipOut, err := exec.Command("adb", tcpipArgs...).CombinedOutput(); err != nil {
			fmt.Printf("Notice: adb tcpip 5555: %s\n", strings.TrimSpace(string(tcpipOut)))
		}
		time.Sleep(1 * time.Second)
	}
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

// findAgentBinary searches candidate directories for a matching agent binary.
func findAgentBinary(abi string) string {
	var names []string
	if strings.Contains(abi, "x86_64") {
		names = []string{
			"adblink-agent-android-amd64",
			"adblink-agent-x86_64",
			"adblink-agent-amd64",
		}
	} else if strings.Contains(abi, "x86") {
		names = []string{
			"adblink-agent-android-386",
			"adblink-agent-x86",
			"adblink-agent-386",
		}
	} else if strings.Contains(abi, "arm") && !strings.Contains(abi, "64") {
		names = []string{
			"adblink-agent-android-arm",
			"adblink-agent-armv7",
			"adblink-agent-arm",
		}
	} else {
		names = []string{
			"adblink-agent-android-arm64",
			"adblink-agent-arm64",
		}
	}
	names = append(names, "adblink-agent")

	searchDirs := []string{
		"bin",
		filepath.Join("bin", "android"),
		".",
	}

	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		searchDirs = append(searchDirs,
			exeDir,
			filepath.Join(exeDir, "android"),
			filepath.Join(exeDir, "bin"),
			filepath.Join(exeDir, "bin", "android"),
		)
	}

	for _, dir := range searchDirs {
		for _, name := range names {
			p := filepath.Join(dir, name)
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				return p
			}
		}
	}
	return ""
}

func cmdAuto(args []string) {
	fs := flag.NewFlagSet("auto", flag.ExitOnError)

	defaultServer := os.Getenv("ADBLINK_SERVER")
	if defaultServer == "" {
		defaultServer = "127.0.0.1:8888"
	}
	serverAddr := fs.String("server", defaultServer, "AdbLink server address")
	adbSerial := fs.String("s", "", "Target ADB device serial")
	agentBin := fs.String("bin", "", "Path to adblink-agent binary")
	token := fs.String("token", "", "Authentication token")
	webAddr := fs.String("web", "", "AdbLink server Web API URL")

	_ = fs.Parse(args)

	// If positional argument provided without flag (e.g. 'adblink-ctl 1.2.3.4:8888')
	if fs.NArg() > 0 {
		pos := fs.Arg(0)
		if !strings.HasPrefix(pos, "-") {
			*serverAddr = pos
		}
	}

	// Format server address if port is omitted
	if !strings.Contains(*serverAddr, ":") {
		*serverAddr = *serverAddr + ":8888"
	}

	// Derive Web API URL if not provided
	if *webAddr == "" {
		host, _, err := net.SplitHostPort(*serverAddr)
		if err != nil {
			host = *serverAddr
		}
		*webAddr = fmt.Sprintf("http://%s:9999", host)
	}

	fmt.Println("==========================================================")
	fmt.Println("       AdbLink One-Click Auto Deploy & Connect            ")
	fmt.Println("==========================================================")

	// Step 1: Detect available device
	targetSerial, model, abi, err := detectTargetDevice(*adbSerial)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[1/5] Target device: %s (%s, ABI: %s)\n", model, targetSerial, abi)

	// Step 2: Enable TCP mode for physical USB devices (adb tcpip 5555)
	if isUSBDevice(targetSerial) {
		fmt.Print("[2/5] Ensuring TCP mode on USB device (adb tcpip 5555)... ")
		_ = exec.Command("adb", "-s", targetSerial, "tcpip", "5555").Run()
		time.Sleep(1 * time.Second)
		// If server address is localhost/127.0.0.1, configure adb reverse so USB-connected device can reach host server
		serverHost, serverPort, _ := net.SplitHostPort(*serverAddr)
		if serverHost == "127.0.0.1" || serverHost == "localhost" {
			_ = exec.Command("adb", "-s", targetSerial, "reverse", fmt.Sprintf("tcp:%s", serverPort), fmt.Sprintf("tcp:%s", serverPort)).Run()
		}
		fmt.Println("Done")
	} else {
		fmt.Println("[2/5] Device is connected wirelessly/network, skipping 'adb tcpip' to preserve connection.")
	}
	// Step 3: Find agent binary
	binPath := *agentBin
	if binPath == "" {
		binPath = findAgentBinary(abi)
	}
	if binPath == "" {
		fmt.Fprintf(os.Stderr, "Error: Could not locate adblink-agent binary for %s.\n", abi)
		fmt.Fprintf(os.Stderr, "Please specify path with: adblink-ctl -bin <path>\n")
		os.Exit(1)
	}
	fmt.Printf("[3/5] Deploying agent (%s)... ", filepath.Base(binPath))

	remotePath := "/data/local/tmp/adblink-agent"
	// Ensure device is responsive
	for range 3 {
		if stateErr := exec.Command("adb", "-s", targetSerial, "get-state").Run(); stateErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if out, err := exec.Command("adb", "-s", targetSerial, "push", binPath, remotePath).CombinedOutput(); err != nil {
		fmt.Println("Failed")
		fmt.Fprintf(os.Stderr, "adb push error: %s\n", string(out))
		os.Exit(1)
	}
	_ = exec.Command("adb", "-s", targetSerial, "shell", "chmod", "+x", remotePath).Run()
	fmt.Println("Done")

	// Step 4: Stop existing agent and launch in background
	fmt.Printf("[4/5] Launching agent in background -> Connecting to %s... ", *serverAddr)
	_ = exec.Command("adb", "-s", targetSerial, "shell", "pkill -9 adblink-agent 2>/dev/null || true").Run()

	launchArgs := []string{"-s", targetSerial, "shell", remotePath, "-server", *serverAddr, "-d"}
	if *token != "" {
		launchArgs = append(launchArgs, "-token", *token)
	}
	if out, err := exec.Command("adb", launchArgs...).CombinedOutput(); err != nil {
		fmt.Println("Failed")
		fmt.Fprintf(os.Stderr, "Failed to start agent: %s\n", string(out))
		os.Exit(1)
	}
	fmt.Println("Done")

	// Step 5: Wait for registration and auto-connect
	fmt.Print("[5/5] Waiting for server registration and tunnel exposure... ")
	time.Sleep(2 * time.Second)

	connectTarget := pollDeviceConnectTarget(*webAddr, targetSerial, model, 5)
	if connectTarget != "" {
		fmt.Printf("Registered on %s!\n", connectTarget)
		fmt.Printf("Connecting host ADB to %s...\n", connectTarget)
		_ = exec.Command("adb", "connect", connectTarget).Run()
	} else {
		fmt.Println("Agent running in background.")
		fmt.Printf("Check status with: adblink-ctl list -server %s\n", *webAddr)
	}

	fmt.Println("==========================================================")
	if connectTarget != "" {
		fmt.Println("✓ SUCCESS! Reverse tunnel active.")
		fmt.Println("  You can now unplug USB and run:")
		fmt.Printf("    adb -s %s shell\n", connectTarget)
	} else {
		fmt.Println("✓ Agent deployed and running on device.")
	}
	fmt.Println("==========================================================")
}

func detectTargetDevice(specifiedSerial string) (serial, model, abi string, err error) {
	if _, lookErr := exec.LookPath("adb"); lookErr != nil {
		return "", "", "", fmt.Errorf("ADB command not found in PATH. Please install Android Platform Tools")
	}

	if specifiedSerial != "" {
		serial = specifiedSerial
	} else {
		out, err := exec.Command("adb", "devices").Output()
		if err != nil {
			return "", "", "", fmt.Errorf("failed to run 'adb devices': %w", err)
		}

		lines := strings.Split(string(out), "\n")
		var candidates []string
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "List of devices") || strings.HasPrefix(line, "*") {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) >= 2 && parts[1] == "device" {
				dev := parts[0]
				candidates = append(candidates, dev)
			}
		}

		if len(candidates) == 0 {
			return "", "", "", fmt.Errorf("no online Android devices found via USB/ADB. Please connect your phone and enable USB Debugging")
		}

		// Filter out existing reverse tunnel ports (55550-55599) if other devices exist
		filtered := []string{}
		for _, c := range candidates {
			isTunnelPort := false
			for p := 55550; p <= 55599; p++ {
				if strings.Contains(c, fmt.Sprintf(":%d", p)) {
					isTunnelPort = true
					break
				}
			}
			if !isTunnelPort {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) == 0 {
			filtered = candidates
		}

		// Prioritize physical USB devices over wireless/mDNS/network devices
		var usbDevices []string
		var netDevices []string
		for _, dev := range filtered {
			if isUSBDevice(dev) {
				usbDevices = append(usbDevices, dev)
			} else {
				netDevices = append(netDevices, dev)
			}
		}

		if len(usbDevices) > 0 {
			serial = usbDevices[0]
		} else {
			serial = netDevices[0]
		}
	}
	mOut, _ := exec.Command("adb", "-s", serial, "shell", "getprop", "ro.product.model").Output()
	model = strings.TrimSpace(string(mOut))
	if model == "" {
		model = "Android Device"
	}

	aOut, _ := exec.Command("adb", "-s", serial, "shell", "getprop", "ro.product.cpu.abi").Output()
	abi = strings.TrimSpace(string(aOut))
	if abi == "" {
		abi = "arm64-v8a"
	}

	return serial, model, abi, nil
}

func pollDeviceConnectTarget(webAddr, serial, model string, maxAttempts int) string {
	client := &http.Client{Timeout: 2 * time.Second}
	url := strings.TrimRight(webAddr, "/") + "/api/v1/devices"

	for range maxAttempts {
		resp, err := client.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			var devices []server.DeviceInfo
			if decodeErr := json.NewDecoder(resp.Body).Decode(&devices); decodeErr == nil {
				resp.Body.Close()
				var online []server.DeviceInfo
				for _, d := range devices {
					if d.Status == "ONLINE" {
						online = append(online, d)
					}
				}
				if len(online) == 1 {
					return online[0].AdbConnectTarget
				}
				for _, d := range online {
					if (serial != "" && strings.Contains(d.DeviceID, serial)) ||
						(model != "" && strings.EqualFold(d.Model, model)) {
						return d.AdbConnectTarget
					}
				}
			} else {
				resp.Body.Close()
			}
		}
		time.Sleep(1 * time.Second)
	}
	return ""
}

// isUSBDevice checks if the ADB device serial represents a physical USB connection,
// as opposed to a network connection (IP:Port) or mDNS TLS service record (._tcp).
func isUSBDevice(serial string) bool {
	if serial == "" {
		return true // Default to true if unspecified
	}
	if strings.Contains(serial, ":") || strings.Contains(serial, "._tcp") || strings.Contains(serial, "._adb") {
		return false
	}
	return true
}

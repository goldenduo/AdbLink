package agent

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DeviceProperties contains detected system properties.
type DeviceProperties struct {
	DeviceID       string
	Model          string
	Manufacturer   string
	AndroidVersion string
}

// getAndroidProp reads an Android system property via /system/bin/getprop.
func getAndroidProp(key string) string {
	cmd := exec.Command("/system/bin/getprop", key)
	out, err := cmd.Output()
	if err != nil {
		// Fallback to getprop in PATH
		cmd = exec.Command("getprop", key)
		out, err = cmd.Output()
		if err != nil {
			return ""
		}
	}
	return strings.TrimSpace(string(out))
}

// randomID generates a random hex identifier.
func randomID(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%x", prefix, b)
}

// DetectProperties inspects the system to discover Android or host hardware properties.
func DetectProperties() DeviceProperties {
	props := DeviceProperties{
		Model:          getAndroidProp("ro.product.model"),
		Manufacturer:   getAndroidProp("ro.product.manufacturer"),
		AndroidVersion: getAndroidProp("ro.build.version.release"),
	}

	// Try serial numbers
	serial := getAndroidProp("ro.serialno")
	if serial == "" {
		serial = getAndroidProp("ro.boot.serialno")
	}
	if serial == "" {
		serial = getAndroidProp("persist.adb.wifi.guid")
	}

	// Fallback to hostname or machine-id
	if serial == "" {
		if data, err := os.ReadFile("/etc/machine-id"); err == nil && len(bytes.TrimSpace(data)) > 0 {
			serial = string(bytes.TrimSpace(data))
			if len(serial) > 16 {
				serial = serial[:16]
			}
		} else if host, err := os.Hostname(); err == nil && host != "" {
			serial = host
		} else {
			serial = randomID("android")
		}
	}

	props.DeviceID = serial

	if props.Model == "" {
		if host, err := os.Hostname(); err == nil {
			props.Model = host
		} else {
			props.Model = "Android-Device"
		}
	}
	if props.AndroidVersion == "" {
		props.AndroidVersion = "unknown"
	}

	return props
}

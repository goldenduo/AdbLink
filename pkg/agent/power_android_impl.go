//go:build android || adblinkandroid

package agent

import (
	"bytes"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
)

// androidPowerKeeper applies best-effort safeguards that are available to an
// adbd shell process. A standalone native binary cannot create an Android
// PowerManager WakeLock because that requires an Android application context
// and the WAKE_LOCK permission. We therefore use the shell interfaces that are
// present on stock Android where permitted, and restore settings on exit.
type androidPowerKeeper struct {
	logger *log.Logger

	mu      sync.Mutex
	started bool
	stopped bool

	wifiSleepPolicy        string
	hasWiFiSleepPolicy     bool
	stayOnWhilePluggedIn   string
	hasStayOnSetting       bool
	restoreDeviceIdle      bool
	termuxWakeLockAcquired bool
}

func newPowerKeeper(logger *log.Logger) *androidPowerKeeper {
	return &androidPowerKeeper{logger: logger}
}

func (p *androidPowerKeeper) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.started {
		return nil
	}
	p.started = true

	if value, ok := androidSetting("global", "wifi_sleep_policy"); ok {
		p.wifiSleepPolicy = value
		p.hasWiFiSleepPolicy = true
	}
	if value, ok := androidSetting("global", "stay_on_while_plugged_in"); ok {
		p.stayOnWhilePluggedIn = value
		p.hasStayOnSetting = true
	}

	// Prevent Doze from suspending the agent's network activity while it is
	// running. This is a shell-level fallback, not a replacement for a real
	// Android foreground service/wakelock.
	if androidDeviceIdleEnabled() {
		if err := runAndroidCommand("cmd", "deviceidle", "disable"); err == nil {
			p.restoreDeviceIdle = true
		} else if err := runAndroidCommand("dumpsys", "deviceidle", "disable"); err == nil {
			p.restoreDeviceIdle = true
		} else {
			p.logger.Printf("keep-awake: unable to disable device idle mode: %v", err)
		}
	}

	// WIFI_SLEEP_POLICY_NEVER (2) keeps an already-enabled Wi-Fi connection
	// associated while the screen is off. It does not force Wi-Fi on when the
	// user deliberately disabled Wi-Fi.
	if err := runAndroidCommand("settings", "put", "global", "wifi_sleep_policy", "2"); err != nil {
		p.logger.Printf("keep-awake: unable to set Wi-Fi sleep policy: %v", err)
	}

	// svc power stayon applies to plugged-in states on Android. It is useful
	// when a phone is deployed on USB power, and the original setting is restored
	// when the agent exits.
	if err := runAndroidCommand("svc", "power", "stayon", "true"); err != nil {
		p.logger.Printf("keep-awake: unable to set plugged-in stay-awake policy: %v", err)
	}

	// Termux exposes a real partial wakelock on devices where it is installed.
	// Use it when available, while keeping the agent fully standalone elsewhere.
	if err := runOptionalCommand("termux-wake-lock"); err == nil {
		p.termuxWakeLockAcquired = true
	} else {
		p.logger.Printf("keep-awake: no Termux wakelock helper available; CPU sleep cannot be fully prevented by a standalone native binary")
	}

	return nil
}

func (p *androidPowerKeeper) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.started || p.stopped {
		return nil
	}
	p.stopped = true

	var errs []error
	if p.termuxWakeLockAcquired {
		if err := runOptionalCommand("termux-wake-unlock"); err != nil {
			errs = append(errs, fmt.Errorf("release Termux wakelock: %w", err))
		}
	}

	if p.hasWiFiSleepPolicy {
		if err := runAndroidCommand("settings", "put", "global", "wifi_sleep_policy", p.wifiSleepPolicy); err != nil {
			errs = append(errs, fmt.Errorf("restore Wi-Fi sleep policy: %w", err))
		}
	} else if err := runAndroidCommand("settings", "delete", "global", "wifi_sleep_policy"); err != nil {
		errs = append(errs, fmt.Errorf("remove temporary Wi-Fi sleep policy: %w", err))
	}

	if p.hasStayOnSetting {
		if err := runAndroidCommand("settings", "put", "global", "stay_on_while_plugged_in", p.stayOnWhilePluggedIn); err != nil {
			errs = append(errs, fmt.Errorf("restore stay-awake policy: %w", err))
		}
	} else if err := runAndroidCommand("settings", "delete", "global", "stay_on_while_plugged_in"); err != nil {
		errs = append(errs, fmt.Errorf("remove temporary stay-awake policy: %w", err))
	}

	if p.restoreDeviceIdle {
		if err := runAndroidCommand("cmd", "deviceidle", "enable"); err != nil {
			if fallbackErr := runAndroidCommand("dumpsys", "deviceidle", "enable"); fallbackErr != nil {
				errs = append(errs, fmt.Errorf("restore device idle mode: %w", fallbackErr))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%v", errs)
	}
	return nil
}

func androidSetting(namespace, key string) (string, bool) {
	out, err := runAndroidCommandOutput("settings", "get", namespace, key)
	if err != nil {
		return "", false
	}
	value := strings.TrimSpace(string(out))
	if value == "" || value == "null" || value == "<null>" {
		return "", false
	}
	return value, true
}

func androidDeviceIdleEnabled() bool {
	out, err := runAndroidCommandOutput("dumpsys", "deviceidle")
	if err != nil {
		return false
	}
	// AOSP exposes mDeepEnabled on current releases. Older releases may only
	// expose mEnabled; both are conservative checks before issuing disable.
	return bytes.Contains(out, []byte("mDeepEnabled=true")) ||
		bytes.Contains(out, []byte("mEnabled=true"))
}

func runAndroidCommand(name string, args ...string) error {
	_, err := runAndroidCommandOutput(name, args...)
	return err
}

func runAndroidCommandOutput(name string, args ...string) ([]byte, error) {
	paths := []string{"/system/bin/" + name, name}
	var lastErr error
	for _, path := range paths {
		cmd := exec.Command(path, args...)
		if out, err := cmd.CombinedOutput(); err == nil {
			return out, nil
		} else {
			lastErr = fmt.Errorf("%s: %w (%s)", path, err, strings.TrimSpace(string(out)))
		}
	}
	return nil, lastErr
}

func runOptionalCommand(name string, args ...string) error {
	paths := []string{
		name,
		"/data/data/com.termux/files/usr/bin/" + name,
		"/data/data/com.termux/files/usr/bin/" + strings.TrimPrefix(name, "termux-"),
	}
	var lastErr error
	for _, path := range paths {
		cmd := exec.Command(path, args...)
		if out, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			lastErr = fmt.Errorf("%s: %w (%s)", path, err, strings.TrimSpace(string(out)))
		}
	}
	return lastErr
}

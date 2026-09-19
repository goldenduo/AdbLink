//go:build !android && !adblinkandroid

package agent

import "log"

// powerKeeper is a no-op outside Android. The agent still benefits from the
// TCP/Yamux heartbeat on desktop platforms, but Android-specific shell
// safeguards must not leak into server builds.
type powerKeeper struct{}

func newPowerKeeper(_ *log.Logger) *powerKeeper {
	return &powerKeeper{}
}

func (*powerKeeper) Start() error { return nil }

func (*powerKeeper) Stop() error { return nil }

//go:build !linux && !android

package main

func maybeDaemonize(enabled bool) {
	// No-op on non-linux systems
}

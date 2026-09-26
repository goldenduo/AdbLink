package main

import (
	"testing"
)

func TestShouldEnableTcpip5555(t *testing.T) {
	tests := []struct {
		serial string
		want   bool
	}{
		{"", true},
		{"ae20de969904", true}, // USB serial
		{"adb-ae20de969904-vHcjK3._adb-tls-connect._tcp", true}, // Wireless TLS mDNS
		{"device-123._adb._tcp", true},
		{"192.168.1.100:5555", false}, // Already 5555
		{"10.0.0.5:5555", false},
		{"192.168.1.100:39485", true}, // Dynamic port wireless
	}

	for _, tt := range tests {
		got := shouldEnableTcpip5555(tt.serial)
		if got != tt.want {
			t.Errorf("shouldEnableTcpip5555(%q) = %v; want %v", tt.serial, got, tt.want)
		}
	}
}

func TestNormalizeServerAddr(t *testing.T) {
	tests := []struct {
		input       string
		wantServer  string
		wantWeb     string
	}{
		{"", "127.0.0.1:8888", "http://127.0.0.1:9999"},
		{"1.2.3.4", "1.2.3.4:8888", "http://1.2.3.4:9999"},
		{"1.2.3.4:8888", "1.2.3.4:8888", "http://1.2.3.4:9999"},
		{"example.com", "example.com:8888", "http://example.com:9999"},
		{"[2603:c024:19:b47e::1]:8888", "[2603:c024:19:b47e::1]:8888", "http://[2603:c024:19:b47e::1]:9999"},
		{"[2603:c024:19:b47e::1]", "[2603:c024:19:b47e::1]:8888", "http://[2603:c024:19:b47e::1]:9999"},
		{"2603:c024:19:b47e::1:8888", "[2603:c024:19:b47e::1]:8888", "http://[2603:c024:19:b47e::1]:9999"},
		{"2603:c024:19:b47e::1", "[2603:c024:19:b47e::1]:8888", "http://[2603:c024:19:b47e::1]:9999"},
	}

	for _, tt := range tests {
		gotServer, gotWeb := normalizeServerAddr(tt.input)
		if gotServer != tt.wantServer || gotWeb != tt.wantWeb {
			t.Errorf("normalizeServerAddr(%q) = (%q, %q); want (%q, %q)",
				tt.input, gotServer, gotWeb, tt.wantServer, tt.wantWeb)
		}
	}
}

package main

import (
	"io"
	"strings"
	"testing"
)

func TestShouldEnableTcpip5555(t *testing.T) {
	tests := []struct {
		serial string
		force  bool
		want   bool
	}{
		{"", false, true},
		{"ae20de969904", false, true}, // USB serial
		{"adb-ae20de969904-vHcjK3._adb-tls-connect._tcp", false, false}, // Wireless TLS mDNS -> skip by default!
		{"adb-ae20de969904-vHcjK3._adb-tls-connect._tcp", true, true},   // Wireless TLS mDNS -> forced
		{"device-123._adb._tcp", false, false},
		{"192.168.1.100:5555", false, false}, // Network device -> skip by default
		{"192.168.1.100:5555", true, true},   // Network device -> forced
	}

	for _, tt := range tests {
		got := shouldEnableTcpip5555(tt.serial, tt.force)
		if got != tt.want {
			t.Errorf("shouldEnableTcpip5555(%q, %v) = %v; want %v", tt.serial, tt.force, got, tt.want)
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

func TestPromptSelectDevice(t *testing.T) {
	// 1. Zero devices -> returns error
	_, err := promptSelectDevice(nil, strings.NewReader(""), io.Discard)
	if err == nil {
		t.Fatal("expected error for zero devices, got nil")
	}

	dev1 := CandidateDevice{Serial: "serial-1", Model: "Xiaomi 14", ABI: "arm64-v8a", IsUSB: true}
	dev2 := CandidateDevice{Serial: "serial-2", Model: "Pixel 8", ABI: "arm64-v8a", IsUSB: false}

	// 2. Single device -> enter/empty selects dev1
	selected, err := promptSelectDevice([]CandidateDevice{dev1}, strings.NewReader("\n"), io.Discard)
	if err != nil || selected.Serial != "serial-1" {
		t.Fatalf("expected serial-1, got %v (err: %v)", selected, err)
	}

	// 3. Multiple devices -> select 2
	selected, err = promptSelectDevice([]CandidateDevice{dev1, dev2}, strings.NewReader("2\n"), io.Discard)
	if err != nil || selected.Serial != "serial-2" {
		t.Fatalf("expected serial-2, got %v (err: %v)", selected, err)
	}

	// 4. Multiple devices -> empty enter defaults to 1
	selected, err = promptSelectDevice([]CandidateDevice{dev1, dev2}, strings.NewReader("\n"), io.Discard)
	if err != nil || selected.Serial != "serial-1" {
		t.Fatalf("expected serial-1 on default, got %v (err: %v)", selected, err)
	}

	// 5. Multiple devices -> invalid then valid
	selected, err = promptSelectDevice([]CandidateDevice{dev1, dev2}, strings.NewReader("invalid\n99\n2\n"), io.Discard)
	if err != nil || selected.Serial != "serial-2" {
		t.Fatalf("expected serial-2 after retry, got %v (err: %v)", selected, err)
	}
}

func TestPromptSelectOrInputServer(t *testing.T) {
	history := []string{"192.168.1.100:8888", "47.98.123.45:8888"}

	// 1. History present -> Enter selects history[0]
	server, err := promptSelectOrInputServer(history, strings.NewReader("\n"), io.Discard)
	if err != nil || server != "192.168.1.100:8888" {
		t.Fatalf("expected 192.168.1.100:8888, got %q (err: %v)", server, err)
	}

	// 2. History present -> "2" selects history[1]
	server, err = promptSelectOrInputServer(history, strings.NewReader("2\n"), io.Discard)
	if err != nil || server != "47.98.123.45:8888" {
		t.Fatalf("expected 47.98.123.45:8888, got %q (err: %v)", server, err)
	}

	// 3. History present -> choose option 3 (manual input), enter IP:Port
	server, err = promptSelectOrInputServer(history, strings.NewReader("3\n10.0.0.1:9000\n"), io.Discard)
	if err != nil || server != "10.0.0.1:9000" {
		t.Fatalf("expected 10.0.0.1:9000, got %q (err: %v)", server, err)
	}

	// 4. No history -> guide input with IP then Port
	server, err = promptSelectOrInputServer(nil, strings.NewReader("10.10.10.10\n8080\n"), io.Discard)
	if err != nil || server != "10.10.10.10:8080" {
		t.Fatalf("expected 10.10.10.10:8080, got %q (err: %v)", server, err)
	}

	// 5. No history -> enter on both IP and port defaults to 127.0.0.1:8888
	server, err = promptSelectOrInputServer(nil, strings.NewReader("\n\n"), io.Discard)
	if err != nil || server != "127.0.0.1:8888" {
		t.Fatalf("expected 127.0.0.1:8888, got %q (err: %v)", server, err)
	}
}

func TestServerHistoryPersistence(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("USERPROFILE", tempDir)

	// Initial history should be empty
	initial := loadServerHistory()
	if len(initial) != 0 {
		t.Fatalf("expected empty initial history, got %v", initial)
	}

	// Record first server
	recordServerToHistory("1.1.1.1:8888")
	h1 := loadServerHistory()
	if len(h1) != 1 || h1[0] != "1.1.1.1:8888" {
		t.Fatalf("expected [1.1.1.1:8888], got %v", h1)
	}

	// Record second server
	recordServerToHistory("2.2.2.2:8888")
	h2 := loadServerHistory()
	if len(h2) != 2 || h2[0] != "2.2.2.2:8888" || h2[1] != "1.1.1.1:8888" {
		t.Fatalf("expected [2.2.2.2:8888, 1.1.1.1:8888], got %v", h2)
	}

	// Re-record first server -> should move to front
	recordServerToHistory("1.1.1.1:8888")
	h3 := loadServerHistory()
	if len(h3) != 2 || h3[0] != "1.1.1.1:8888" || h3[1] != "2.2.2.2:8888" {
		t.Fatalf("expected [1.1.1.1:8888, 2.2.2.2:8888], got %v", h3)
	}
}

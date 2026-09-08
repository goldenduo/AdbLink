package protocol

import (
	"bytes"
	"testing"
)

func TestProtocolSerialization(t *testing.T) {
	req := RegisterRequest{
		Magic:          MagicHeader,
		Version:        CurrentProtocolVersion,
		DeviceID:       "device-12345",
		Model:          "Pixel 8",
		Manufacturer:   "Google",
		AndroidVersion: "15",
		Token:          "secret-token",
		RequestedPort:  55555,
		ClientVersion:  "1.0.0",
	}

	buf := new(bytes.Buffer)
	if err := WriteMsg(buf, req); err != nil {
		t.Fatalf("WriteMsg failed: %v", err)
	}

	var decodedReq RegisterRequest
	if err := ReadMsg(buf, &decodedReq); err != nil {
		t.Fatalf("ReadMsg failed: %v", err)
	}

	if decodedReq.Magic != req.Magic {
		t.Errorf("Magic mismatch: got %x, want %x", decodedReq.Magic, req.Magic)
	}
	if decodedReq.DeviceID != req.DeviceID {
		t.Errorf("DeviceID mismatch: got %s, want %s", decodedReq.DeviceID, req.DeviceID)
	}
	if decodedReq.RequestedPort != req.RequestedPort {
		t.Errorf("RequestedPort mismatch: got %d, want %d", decodedReq.RequestedPort, req.RequestedPort)
	}
}

func TestResponseSerialization(t *testing.T) {
	resp := RegisterResponse{
		Magic:         MagicHeader,
		Version:       CurrentProtocolVersion,
		Status:        StatusOK,
		Message:       "Welcome",
		AssignedPort:  55555,
		ServerVersion: "1.0.0",
		AdvertiseHost: "192.168.1.100",
	}

	buf := new(bytes.Buffer)
	if err := WriteMsg(buf, resp); err != nil {
		t.Fatalf("WriteMsg failed: %v", err)
	}

	var decodedResp RegisterResponse
	if err := ReadMsg(buf, &decodedResp); err != nil {
		t.Fatalf("ReadMsg failed: %v", err)
	}

	if decodedResp.Status != resp.Status {
		t.Errorf("Status mismatch: got %v, want %v", decodedResp.Status, resp.Status)
	}
	if decodedResp.AssignedPort != resp.AssignedPort {
		t.Errorf("AssignedPort mismatch: got %d, want %d", decodedResp.AssignedPort, resp.AssignedPort)
	}
	if decodedResp.AdvertiseHost != resp.AdvertiseHost {
		t.Errorf("AdvertiseHost mismatch: got %s, want %s", decodedResp.AdvertiseHost, resp.AdvertiseHost)
	}
}

package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Protocol constants
const (
	// MagicHeader is a 4-byte header identifying the AdbLink protocol: "ADBL"
	MagicHeader uint32 = 0x4144424C

	// CurrentProtocolVersion is the protocol version
	CurrentProtocolVersion uint32 = 1

	// MaxMessageSize limits the handshake message size to 1MB to prevent abuse
	MaxMessageSize uint32 = 1024 * 1024
)

// Status codes for responses
type StatusCode string

const (
	StatusOK              StatusCode = "OK"
	StatusUnauthorized    StatusCode = "UNAUTHORIZED"
	StatusPortUnavailable StatusCode = "PORT_UNAVAILABLE"
	StatusDeviceConflict  StatusCode = "DEVICE_CONFLICT"
	StatusError           StatusCode = "ERROR"
)

// RegisterRequest is sent by the Agent upon establishing the persistent connection.
type RegisterRequest struct {
	Magic          uint32 `json:"magic"`
	Version        uint32 `json:"version"`
	DeviceID       string `json:"device_id"`
	Model          string `json:"model,omitempty"`
	Manufacturer   string `json:"manufacturer,omitempty"`
	AndroidVersion string `json:"android_version,omitempty"`
	Token          string `json:"token,omitempty"`
	RequestedPort  int    `json:"requested_port,omitempty"` // 0 means server allocates
	ClientVersion  string `json:"client_version,omitempty"`
}

// RegisterResponse is sent by the Server to accept or reject the agent registration.
type RegisterResponse struct {
	Magic         uint32     `json:"magic"`
	Version       uint32     `json:"version"`
	Status        StatusCode `json:"status"`
	Message       string     `json:"message,omitempty"`
	AssignedPort  int        `json:"assigned_port,omitempty"`
	ServerVersion string     `json:"server_version,omitempty"`
	AdvertiseHost string     `json:"advertise_host,omitempty"`
}

// WriteMsg encodes and writes a length-prefixed JSON message.
// Format: [4 bytes BigEndian length][JSON payload]
func WriteMsg(w io.Writer, msg interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message error: %w", err)
	}

	length := uint32(len(data))
	if length > MaxMessageSize {
		return fmt.Errorf("message size %d exceeds max %d", length, MaxMessageSize)
	}

	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, length)

	if _, err := w.Write(lenBuf); err != nil {
		return fmt.Errorf("write message length error: %w", err)
	}

	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write message payload error: %w", err)
	}

	return nil
}

// ReadMsg reads and decodes a length-prefixed JSON message.
func ReadMsg(r io.Reader, msg interface{}) error {
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, lenBuf); err != nil {
		return fmt.Errorf("read message length error: %w", err)
	}

	length := binary.BigEndian.Uint32(lenBuf)
	if length > MaxMessageSize {
		return fmt.Errorf("message size %d exceeds max allowed %d", length, MaxMessageSize)
	}
	if length == 0 {
		return errors.New("empty message received")
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return fmt.Errorf("read message payload error: %w", err)
	}

	if err := json.Unmarshal(data, msg); err != nil {
		return fmt.Errorf("unmarshal message error: %w", err)
	}

	return nil
}

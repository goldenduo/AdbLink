package server

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrNoPortAvailable     = errors.New("no ports available in configured range")
	ErrPortOutOfRange      = errors.New("requested port is out of allowed range")
	ErrPortAlreadyAssigned = errors.New("requested port is already assigned to another device")
)

type portReservation struct {
	deviceID  string
	expiresAt time.Time
}

// PortPool manages a pool of TCP ports for exposing ADB services.
type PortPool struct {
	minPort      int
	maxPort      int
	mu           sync.Mutex
	assigned     map[int]string          // port -> deviceID
	reservations map[int]portReservation // port -> reservation (grace period)
}

// NewPortPool initializes a new PortPool with the given port range.
func NewPortPool(minPort, maxPort int) (*PortPool, error) {
	if minPort <= 0 || maxPort <= 0 || minPort > maxPort {
		return nil, fmt.Errorf("invalid port range: %d-%d", minPort, maxPort)
	}
	return &PortPool{
		minPort:      minPort,
		maxPort:      maxPort,
		assigned:     make(map[int]string),
		reservations: make(map[int]portReservation),
	}, nil
}

// cleanExpiredReservationsLocked purges expired port reservations.
func (p *PortPool) cleanExpiredReservationsLocked() {
	now := time.Now()
	for port, res := range p.reservations {
		if now.After(res.expiresAt) {
			delete(p.reservations, port)
		}
	}
}

// Acquire allocates a port for a device.
// If requestedPort > 0, it attempts to assign that specific port.
// Otherwise, it assigns the first free port in range, prioritizing any active reservation for this device.
func (p *PortPool) Acquire(requestedPort int, deviceID string) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.cleanExpiredReservationsLocked()

	// If a specific port was requested
	if requestedPort > 0 {
		if requestedPort < p.minPort || requestedPort > p.maxPort {
			return 0, ErrPortOutOfRange
		}
		if owner, inUse := p.assigned[requestedPort]; inUse {
			if owner != deviceID {
				return 0, ErrPortAlreadyAssigned
			}
		}
		if res, reserved := p.reservations[requestedPort]; reserved {
			if res.deviceID != deviceID {
				return 0, ErrPortAlreadyAssigned
			}
			delete(p.reservations, requestedPort)
		}
		p.assigned[requestedPort] = deviceID
		return requestedPort, nil
	}

	// Check if this device has an existing reservation
	for port, res := range p.reservations {
		if res.deviceID == deviceID {
			delete(p.reservations, port)
			p.assigned[port] = deviceID
			return port, nil
		}
	}

	// Check if this device already has an assigned port
	for port, owner := range p.assigned {
		if owner == deviceID {
			return port, nil
		}
	}

	// Find the first available port
	for port := p.minPort; port <= p.maxPort; port++ {
		_, inUse := p.assigned[port]
		_, reserved := p.reservations[port]
		if !inUse && !reserved {
			p.assigned[port] = deviceID
			return port, nil
		}
	}

	return 0, ErrNoPortAvailable
}

// Release frees the port immediately.
func (p *PortPool) Release(port int, deviceID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if owner, ok := p.assigned[port]; ok && owner == deviceID {
		delete(p.assigned, port)
	}
	if res, ok := p.reservations[port]; ok && res.deviceID == deviceID {
		delete(p.reservations, port)
	}
}

// ReleaseWithGracePeriod reserves the port for the device for a grace period.
func (p *PortPool) ReleaseWithGracePeriod(port int, deviceID string, grace time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if owner, ok := p.assigned[port]; ok && owner == deviceID {
		delete(p.assigned, port)
		p.reservations[port] = portReservation{
			deviceID:  deviceID,
			expiresAt: time.Now().Add(grace),
		}
	}
}

// ListAllocations returns a map of currently assigned ports to device IDs.
func (p *PortPool) ListAllocations() map[int]string {
	p.mu.Lock()
	defer p.mu.Unlock()

	allocs := make(map[int]string, len(p.assigned))
	for k, v := range p.assigned {
		allocs[k] = v
	}
	return allocs
}

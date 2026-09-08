package server

import (
	"testing"
	"time"
)

func TestPortPoolAcquireAndRelease(t *testing.T) {
	pool, err := NewPortPool(55000, 55002)
	if err != nil {
		t.Fatalf("NewPortPool failed: %v", err)
	}

	p1, err := pool.Acquire(0, "dev1")
	if err != nil || p1 != 55000 {
		t.Fatalf("unexpected p1: %d, err: %v", p1, err)
	}

	p2, err := pool.Acquire(0, "dev2")
	if err != nil || p2 != 55001 {
		t.Fatalf("unexpected p2: %d, err: %v", p2, err)
	}

	p3, err := pool.Acquire(0, "dev3")
	if err != nil || p3 != 55002 {
		t.Fatalf("unexpected p3: %d, err: %v", p3, err)
	}

	// Pool full
	_, err = pool.Acquire(0, "dev4")
	if err != ErrNoPortAvailable {
		t.Fatalf("expected ErrNoPortAvailable, got: %v", err)
	}

	// Release p2 and reacquire
	pool.Release(p2, "dev2")
	p4, err := pool.Acquire(0, "dev4")
	if err != nil || p4 != 55001 {
		t.Fatalf("expected dev4 to get 55001, got: %d, err: %v", p4, err)
	}
}

func TestPortPoolGracePeriod(t *testing.T) {
	pool, err := NewPortPool(55000, 55001)
	if err != nil {
		t.Fatalf("NewPortPool failed: %v", err)
	}

	p1, err := pool.Acquire(0, "dev1")
	if err != nil || p1 != 55000 {
		t.Fatalf("unexpected p1: %d, err: %v", p1, err)
	}

	// Release with 50ms grace period
	pool.ReleaseWithGracePeriod(p1, "dev1", 50*time.Millisecond)

	// Another device trying to acquire p1 specifically should fail during grace period
	_, err = pool.Acquire(p1, "dev2")
	if err != ErrPortAlreadyAssigned {
		t.Fatalf("expected ErrPortAlreadyAssigned, got: %v", err)
	}

	// dev1 re-acquires should get the same port
	reP1, err := pool.Acquire(0, "dev1")
	if err != nil || reP1 != p1 {
		t.Fatalf("expected dev1 to get back %d, got %d, err: %v", p1, reP1, err)
	}

	// Now release with grace period and wait for expiration
	pool.ReleaseWithGracePeriod(reP1, "dev1", 10*time.Millisecond)
	time.Sleep(20 * time.Millisecond)

	// Now dev2 should be able to get 55000
	pDev2, err := pool.Acquire(55000, "dev2")
	if err != nil || pDev2 != 55000 {
		t.Fatalf("expected dev2 to get 55000 after expiration, got %d, err: %v", pDev2, err)
	}
}

func TestPortPoolRequestedPort(t *testing.T) {
	pool, err := NewPortPool(55000, 55005)
	if err != nil {
		t.Fatalf("NewPortPool failed: %v", err)
	}

	// Out of range
	_, err = pool.Acquire(50000, "dev1")
	if err != ErrPortOutOfRange {
		t.Fatalf("expected ErrPortOutOfRange, got %v", err)
	}

	// Specific in-range
	p, err := pool.Acquire(55003, "dev1")
	if err != nil || p != 55003 {
		t.Fatalf("expected 55003, got %d, err %v", p, err)
	}

	// Collision
	_, err = pool.Acquire(55003, "dev2")
	if err != ErrPortAlreadyAssigned {
		t.Fatalf("expected ErrPortAlreadyAssigned, got %v", err)
	}
}

package processutil

import (
	"context"
	"io"
	"log"
	"sync"
	"testing"
	"time"
)

func TestProcessMonitorGuardLoopStartsWhenMissing(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	findCalls := 0
	started := make(chan string, 1)
	done := make(chan error, 1)

	m := &ProcessMonitor{
		findPIDsByName: func(name string) ([]int32, error) {
			mu.Lock()
			defer mu.Unlock()
			findCalls++
			if findCalls == 1 {
				return nil, nil
			}
			return []int32{123}, nil
		},
		startCommand: func(command string) error {
			started <- command
			cancel()
			return nil
		},
	}

	go func() {
		done <- m.GuardLoop(ctx, "demo-worker", "demo-worker --serve", time.Hour, log.New(io.Discard, "", 0))
	}()

	select {
	case cmd := <-started:
		if cmd != "demo-worker --serve" {
			t.Fatalf("unexpected start command: %q", cmd)
		}
	case <-time.After(time.Second):
		t.Fatal("guard loop did not start missing process")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("guard loop returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("guard loop did not exit after cancel")
	}
}

func TestProcessMonitorGuardLoopUsesNameAsDefaultCommand(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan string, 1)
	m := &ProcessMonitor{
		findPIDsByName: func(name string) ([]int32, error) {
			return nil, nil
		},
		startCommand: func(command string) error {
			started <- command
			cancel()
			return nil
		},
	}

	if err := m.GuardLoop(ctx, "demo-worker", "", time.Hour, log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("guard loop returned error: %v", err)
	}

	select {
	case cmd := <-started:
		if cmd != "demo-worker" {
			t.Fatalf("unexpected default command: %q", cmd)
		}
	default:
		t.Fatal("guard loop did not invoke starter")
	}
}

func TestProcessMonitorGuardLoopSkipsWhenProcessExists(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := false
	m := &ProcessMonitor{
		findPIDsByName: func(name string) ([]int32, error) {
			cancel()
			return []int32{456}, nil
		},
		startCommand: func(command string) error {
			started = true
			return nil
		},
	}

	if err := m.GuardLoop(ctx, "demo-worker", "demo-worker", time.Hour, log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("guard loop returned error: %v", err)
	}
	if started {
		t.Fatal("guard loop should not start an existing process")
	}
}

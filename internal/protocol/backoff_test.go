// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"context"
	"testing"
	"time"
)

func TestBackoff_ExponentialGrowth(t *testing.T) {
	b := &Backoff{Min: 10 * time.Millisecond, Max: 200 * time.Millisecond}

	var durations []time.Duration
	for range 5 {
		start := time.Now()
		b.Wait(context.Background())
		durations = append(durations, time.Since(start))
	}

	if durations[4] < durations[0] {
		t.Errorf("no exponential growth: first=%v last=%v", durations[0], durations[4])
	}
}

func TestBackoff_NeverExceedsMax(t *testing.T) {
	b := &Backoff{Min: time.Millisecond, Max: 50 * time.Millisecond}

	for range 20 {
		start := time.Now()
		b.Wait(context.Background())
		d := time.Since(start)
		if d > 80*time.Millisecond {
			t.Fatalf("wait %v exceeded max %v", d, b.Max)
		}
	}
}

func TestBackoff_Reset(t *testing.T) {
	b := &Backoff{Min: time.Millisecond, Max: time.Second}

	for range 10 {
		b.Wait(context.Background())
	}

	b.Reset()

	start := time.Now()
	b.Wait(context.Background())
	d := time.Since(start)

	if d > 10*time.Millisecond {
		t.Fatalf("after Reset, wait %v is too long", d)
	}
}

func TestBackoff_CancelledContext(t *testing.T) {
	b := &Backoff{Min: time.Hour, Max: time.Hour}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	ok := b.Wait(ctx)
	d := time.Since(start)

	if ok {
		t.Fatal("Wait returned true on cancelled context")
	}
	if d > 10*time.Millisecond {
		t.Fatalf("Wait took %v on cancelled context", d)
	}
}

package limiters

import (
	"context"
	"testing"
	"time"
)

func TestGateCapsActiveCallers(t *testing.T) {
	gate := New(1)
	release, ok := gate.TryAcquire()
	if !ok {
		t.Fatal("first caller was not admitted")
	}
	if _, ok := gate.TryAcquire(); ok {
		t.Fatal("second caller bypassed the limit")
	}
	done := make(chan error, 1)
	go func() {
		permit, err := gate.Acquire(context.Background())
		if err == nil {
			permit()
		}
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("waiting caller entered before release")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting caller did not enter after release")
	}
}

func TestGateWaitCanCoordinateAnotherResource(t *testing.T) {
	gate := New(1)
	release, _ := gate.TryAcquire()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		if err := gate.Wait(ctx); err != nil {
			return
		}
		permit, ok := gate.TryAcquire()
		if ok {
			permit()
		}
	}()
	time.Sleep(10 * time.Millisecond)
	release()
	if err := gate.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestGateCancellationRemovesWaiter(t *testing.T) {
	gate := New(1)
	release, _ := gate.TryAcquire()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := gate.Acquire(ctx); err == nil {
		t.Fatal("acquire completed without a release")
	}
	if stats := gate.Stats(); stats.Waiting != 0 || stats.Active != 1 {
		t.Fatalf("stats=%#v", stats)
	}
	release()
}

func TestGateResizeWakesWaiters(t *testing.T) {
	gate := New(1)
	release, _ := gate.TryAcquire()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		permit, err := gate.Acquire(ctx)
		if err == nil {
			permit()
		}
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	gate.SetLimit(2)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	release()
}

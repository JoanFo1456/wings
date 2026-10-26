//go:build windows

package cmd

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

// runExecute drives wingsService.Execute the way the service manager does and
// returns what it reported.
func runExecute(t *testing.T, run func() error, send func(requests chan<- svc.ChangeRequest)) (exitSpecific bool, exitCode uint32, states []svc.State) {
	t.Helper()

	requests := make(chan svc.ChangeRequest, 4)
	status := make(chan svc.Status, 16)
	type result struct {
		specific bool
		code     uint32
	}
	done := make(chan result, 1)
	go func() {
		specific, code := (&wingsService{run: run}).Execute(nil, requests, status)
		done <- result{specific, code}
	}()

	if send != nil {
		send(requests)
	}

	select {
	case r := <-done:
		close(status)
		for s := range status {
			states = append(states, s.State)
		}
		return r.specific, r.code, states
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return")
		return
	}
}

func TestServiceStopRequest(t *testing.T) {
	block := make(chan struct{})
	defer close(block)

	specific, code, states := runExecute(t, func() error { <-block; return nil }, func(r chan<- svc.ChangeRequest) {
		r <- svc.ChangeRequest{Cmd: svc.Interrogate, CurrentStatus: svc.Status{State: svc.Running}}
		r <- svc.ChangeRequest{Cmd: svc.Stop}
	})
	if specific || code != 0 {
		t.Errorf("a requested stop must exit cleanly, got specific=%v code=%d", specific, code)
	}
	want := []svc.State{svc.StartPending, svc.Running, svc.Running, svc.StopPending}
	if !reflect.DeepEqual(states, want) {
		t.Errorf("reported states = %v, want %v", states, want)
	}
}

func TestServiceShutdownRequest(t *testing.T) {
	block := make(chan struct{})
	defer close(block)

	_, code, states := runExecute(t, func() error { <-block; return nil }, func(r chan<- svc.ChangeRequest) {
		r <- svc.ChangeRequest{Cmd: svc.Shutdown}
	})
	if code != 0 || states[len(states)-1] != svc.StopPending {
		t.Errorf("shutdown not handled: code=%d states=%v", code, states)
	}
}

// When Wings stops by itself with an error, for example because Docker is not
// reachable yet, the service has to report a failure so that it is restarted.
func TestServiceReportsFailureWhenWingsFails(t *testing.T) {
	specific, code, _ := runExecute(t, func() error { return errors.New("couldn't reach docker") }, nil)
	if !specific || code == 0 {
		t.Errorf("a failing Wings must be reported as a failure, got specific=%v code=%d", specific, code)
	}
}

func TestServiceStopsWhenWingsReturns(t *testing.T) {
	specific, code, _ := runExecute(t, func() error { return nil }, nil)
	if specific || code != 0 {
		t.Errorf("got specific=%v code=%d", specific, code)
	}
}

func TestServiceArgs(t *testing.T) {
	if got := serviceArgs(false, `C:\x\config.yml`); got != nil {
		t.Errorf("default location must not add arguments, got %v", got)
	}
	want := []string{"--config", `C:\x\config.yml`}
	if got := serviceArgs(true, `C:\x\config.yml`); !reflect.DeepEqual(got, want) {
		t.Errorf("serviceArgs = %v, want %v", got, want)
	}
}

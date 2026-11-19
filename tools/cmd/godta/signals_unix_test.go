//go:build unix

package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// TestSignalsCancel: the context of main is canceled by SIGINT and by
// SIGTERM, instead of the process dying with the indexes on the server.
func TestSignalsCancel(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		ctx, stop := signalContext()
		if err := syscall.Kill(os.Getpid(), sig); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Errorf("%v: the context was not canceled", sig)
		}
		stop()
	}
}

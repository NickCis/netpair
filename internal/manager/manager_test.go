package manager

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nickcis/netpair/internal/protocol"
)

// startTestManager runs a Manager on a temp socket outside any pair
// namespace and returns the socket path plus a shutdown function.
func startTestManager(t *testing.T) (string, func()) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "manager.sock")
	done := make(chan error, 1)
	go func() { done <- Run("test", sock) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("manager socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}

	stop := func() {
		_, _ = protocol.Call(sock, protocol.Request{Op: "shutdown"})
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("manager did not stop")
		}
	}
	return sock, stop
}

// TestExecStreamsOutputAndExitCode checks the "exec" op: the ack carries id
// and pid, stdout and stderr are merged into the raw stream, and "wait"
// returns the exit code.
func TestExecStreamsOutputAndExitCode(t *testing.T) {
	sock, stop := startTestManager(t)
	defer stop()

	if v, err := protocol.ManagerVersion(sock); err != nil || v != protocol.Version {
		t.Fatalf("ManagerVersion = %d, %v; want %d, nil", v, err, protocol.Version)
	}

	conn, br, resp, err := protocol.Dial(sock, protocol.Request{
		Op:   "exec",
		Argv: []string{"/bin/sh", "-c", "echo out; echo err >&2; exit 3"},
	})
	if err != nil {
		t.Fatalf("exec dial: %v", err)
	}
	defer conn.Close()
	if !resp.OK {
		t.Fatalf("exec failed: %s", resp.Error)
	}
	if resp.ID == "" || resp.PID == 0 {
		t.Fatalf("ack missing id/pid: %+v", resp)
	}

	out, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if got, want := string(out), "out\nerr\n"; got != want {
		t.Fatalf("stream output = %q, want %q", got, want)
	}

	wr, err := protocol.Call(sock, protocol.Request{Op: "wait", ID: resp.ID})
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !wr.OK || wr.ExitCode != 3 {
		t.Fatalf("wait = %+v, want exit code 3", wr)
	}
}

// TestExecForwardsStdin checks that client bytes reach the process's stdin
// and that a half-close turns into EOF for the process.
func TestExecForwardsStdin(t *testing.T) {
	sock, stop := startTestManager(t)
	defer stop()

	conn, br, resp, err := protocol.Dial(sock, protocol.Request{Op: "exec", Argv: []string{"/bin/cat"}})
	if err != nil {
		t.Fatalf("exec dial: %v", err)
	}
	defer conn.Close()
	if !resp.OK {
		t.Fatalf("exec failed: %s", resp.Error)
	}

	if _, err := io.WriteString(conn, "hello stdin\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatalf("half-close: %v", err)
	}

	out, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if got, want := string(out), "hello stdin\n"; got != want {
		t.Fatalf("stream output = %q, want %q", got, want)
	}

	wr, err := protocol.Call(sock, protocol.Request{Op: "wait", ID: resp.ID})
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !wr.OK || wr.ExitCode != 0 {
		t.Fatalf("wait = %+v, want exit code 0", wr)
	}
}

// TestSignalOp checks that "signal" delivers the requested signal: the
// shell traps TERM and exits 42 (SIGKILL would give -1).
func TestSignalOp(t *testing.T) {
	sock, stop := startTestManager(t)
	defer stop()

	ready := filepath.Join(t.TempDir(), "ready")
	script := "trap 'exit 42' TERM; touch " + ready + "; while :; do sleep 0.05; done"
	conn, br, resp, err := protocol.Dial(sock, protocol.Request{
		Op:   "exec",
		Argv: []string{"/bin/sh", "-c", script},
	})
	if err != nil {
		t.Fatalf("exec dial: %v", err)
	}
	defer conn.Close()
	if !resp.OK {
		t.Fatalf("exec failed: %s", resp.Error)
	}
	go func() { _, _ = io.Copy(io.Discard, br) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("process never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	sr, err := protocol.Call(sock, protocol.Request{Op: "signal", ID: resp.ID, Signal: int(syscall.SIGTERM)})
	if err != nil {
		t.Fatalf("signal: %v", err)
	}
	if !sr.OK {
		t.Fatalf("signal failed: %s", sr.Error)
	}
	wr, err := protocol.Call(sock, protocol.Request{Op: "wait", ID: resp.ID})
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !wr.OK || wr.ExitCode != 42 {
		t.Fatalf("wait = %+v, want exit code 42 (TERM trap)", wr)
	}
}

// TestSignalRequiresSignal checks that "signal" rejects a zero signal
// instead of silently falling back to SIGKILL.
func TestSignalRequiresSignal(t *testing.T) {
	sock, stop := startTestManager(t)
	defer stop()

	resp, err := protocol.Call(sock, protocol.Request{Op: "signal", ID: "proc-1"})
	if err != nil {
		t.Fatalf("signal: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "signal required") {
		t.Fatalf("signal = %+v, want a 'signal required' error", resp)
	}
}

// TestWaitBlocksUntilExit checks that "wait" can be issued while the
// process is still running and returns once it exits.
func TestWaitBlocksUntilExit(t *testing.T) {
	sock, stop := startTestManager(t)
	defer stop()

	conn, br, resp, err := protocol.Dial(sock, protocol.Request{
		Op:   "exec",
		Argv: []string{"/bin/sh", "-c", "sleep 0.3; exit 5"},
	})
	if err != nil {
		t.Fatalf("exec dial: %v", err)
	}
	defer conn.Close()
	if !resp.OK {
		t.Fatalf("exec failed: %s", resp.Error)
	}

	// Drain the stream in the background; wait must block on its own.
	go func() { _, _ = io.Copy(io.Discard, br) }()

	start := time.Now()
	wr, err := protocol.Call(sock, protocol.Request{Op: "wait", ID: resp.ID})
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !wr.OK || wr.ExitCode != 5 {
		t.Fatalf("wait = %+v, want exit code 5", wr)
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("wait returned after %v, before the process exited", elapsed)
	}
}

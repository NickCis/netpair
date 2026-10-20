package pair

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAliveZombie spawns a child that exits twice and does not reap it, so
// it lingers as a zombie. Alive() must report death for the zombie.
func TestAliveZombie(t *testing.T) {
	child := exec.Command("/bin/sh", "-c", "exit 0")
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if Alive(child.Process.Pid) {
			time.Sleep(20 * time.Millisecond)
			if time.Now().After(deadline) {
				t.Fatalf("alive() never reported zombie child %d as dead", child.Process.Pid)
			}
			continue
		}
		break
	}

	// A live process must still be reported alive.
	sleep := exec.Command("/bin/sh", "-c", "sleep 1.5")
	if err := sleep.Start(); err != nil {
		t.Fatalf("start sleep child: %v", err)
	}
	defer func() { _ = sleep.Process.Kill(); _ = sleep.Wait() }()
	time.Sleep(100 * time.Millisecond)
	if !Alive(sleep.Process.Pid) {
		t.Fatalf("alive() reported live child %d as dead", sleep.Process.Pid)
	}
}

// safeBuf is a mutex-protected buffer, so a test can read it while a
// tailer goroutine is still writing to it.
type safeBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestStartLogTail verifies the verbose-mode tailer: existing content is
// streamed to the writer, content appended later is picked up, and stop()
// terminates the tailer quickly and idempotently.
func TestStartLogTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(path, []byte("line1\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	var out safeBuf
	stop := startLogTail(path, &out)
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(out.String(), "line1\n") {
		if time.Now().After(deadline) {
			t.Fatalf("tail never streamed existing line, got %q", out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if _, err := f.WriteString("line2\n"); err != nil {
		t.Fatalf("append log: %v", err)
	}
	f.Close()

	for !strings.Contains(out.String(), "line2\n") {
		if time.Now().After(deadline) {
			t.Fatalf("tail never streamed appended line, got %q", out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A line written right before stop() must still be flushed by the
	// tailer's final drain, even if no polling round saw it.
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if _, err := f.WriteString("line3\n"); err != nil {
		t.Fatalf("append log: %v", err)
	}
	f.Close()

	start := time.Now()
	stop()
	stop() // idempotent
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("stop took %v, expected a fast drain", d)
	}
	if !strings.Contains(out.String(), "line3\n") {
		t.Fatalf("final drain lost a line written before stop(), got %q", out.String())
	}
}

// TestUsernsDenialPatterns checks the pasta log heuristics.
func TestUsernsDenialPatterns(t *testing.T) {
	cases := map[string]bool{
		"Couldn't write to /proc/self/uid_map: Operation not permitted\n": true,
		"Couldn't create user namespace: Permission denied":               true,
		"clone: Operation not permitted":                                  true,
		"DENIED":                                                          true,
		"Use the --map-current-user option":                               false,
		"":                                                                false,
	}
	for in, want := range cases {
		if got := usernsDenial(in); got != want {
			t.Errorf("usernsDenial(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestHasLoopbackNameserver verifies the stub detection used to decide
// whether the pair's resolv.conf needs fixing.
func TestHasLoopbackNameserver(t *testing.T) {
	cases := map[string]bool{
		"nameserver 127.0.0.53\noptions edns0\n":               true,
		"nameserver 127.0.0.53":                                true,
		"nameserver 1.1.1.1\n":                                 false,
		"nameserver 186.130.128.250\nnameserver 192.168.1.1\n": false,
		"# comment\nnameserver ::1\n":                          true,
		"":                                                     false,
	}
	for in, want := range cases {
		if got := hasLoopbackNameserver(in); got != want {
			t.Errorf("hasLoopbackNameserver(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestTailFile checks the pasta log tail helper.
func TestTailFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")

	if _, err := tailFile(path, 10); err == nil {
		t.Fatal("tailFile on missing file should error")
	}

	content := strings.Repeat("0123456789", 100) + "\nlast line\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	all, err := tailFile(path, 1<<20)
	if err != nil || all != content {
		t.Fatalf("small-max tail mismatch: %q", all)
	}

	tail, err := tailFile(path, 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) > 64 {
		t.Fatalf("tail too long: %d bytes", len(tail))
	}
	if !strings.HasSuffix(tail, "last line\n") {
		t.Fatalf("tail missing last line: %q", tail)
	}
}

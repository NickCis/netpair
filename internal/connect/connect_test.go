package connect

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickcis/netpair/internal/manager"
	"github.com/nickcis/netpair/internal/protocol"
)

func TestParseSSOutput(t *testing.T) {
	output := `LISTEN 0 128 127.0.0.1:8080 0.0.0.0:*
LISTEN 0 128 127.0.0.1:9000 0.0.0.0:*
LISTEN 0 128 0.0.0.0:22 0.0.0.0:*
LISTEN 0 128 [::1]:8080 [::]:*`

	maps := parseSSOutput(output)
	if len(maps) != 2 {
		t.Fatalf("expected 2 ports, got %d: %+v", len(maps), maps)
	}
	if maps[0].NSPort != 8080 || maps[1].NSPort != 9000 {
		t.Fatalf("unexpected ports: %+v", maps)
	}
}

// TestDiscoverPorts runs the streaming "exec" op through a real manager and
// checks that ss output is parsed back into port mappings.
func TestDiscoverPorts(t *testing.T) {
	if _, err := exec.LookPath("ss"); err != nil {
		t.Skip("ss not installed")
	}

	sock := filepath.Join(t.TempDir(), "manager.sock")
	done := make(chan error, 1)
	go func() { done <- manager.Run("test", sock, os.Getuid(), os.Getgid()) }()
	defer func() {
		_, _ = protocol.Call(sock, protocol.Request{Op: "shutdown"})
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("manager did not stop")
		}
	}()

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

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	maps, err := discoverPorts(sock)
	if err != nil {
		t.Fatalf("discoverPorts: %v", err)
	}
	for _, m := range maps {
		if m.NSPort == port {
			return
		}
	}
	t.Fatalf("listening port %d not discovered in %+v", port, maps)
}

func TestParsePortSpec(t *testing.T) {
	m, err := ParsePortSpec("8080:19080")
	if err != nil {
		t.Fatal(err)
	}
	if m.NSPort != 8080 || m.HostPort != 19080 {
		t.Fatalf("unexpected map: %+v", m)
	}
}

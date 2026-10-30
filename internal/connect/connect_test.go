package connect

import (
	"bufio"
	"io"
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

// TestRelayPropagatesClientHalfClose guards the Firestore WebChannel case:
// the client half-closes its request, and the manager/target must see EOF so
// it can answer. Without CloseWrite on the manager socket the target never
// receives the FIN and the relay deadlocks.
func TestRelayPropagatesClientHalfClose(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "manager.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	managerDone := make(chan struct{})
	go func() {
		defer close(managerDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

		br := bufio.NewReader(conn)
		req, err := protocol.ReadRequest(br)
		if err != nil || req.Op != "relay" {
			return
		}
		if err := protocol.WriteResponse(conn, protocol.Response{OK: true}); err != nil {
			return
		}

		buf := make([]byte, 1024)
		for {
			n, err := br.Read(buf)
			if n > 0 {
				_, _ = conn.Write(buf[:n])
			}
			if err == io.EOF {
				_, _ = conn.Write([]byte("EOF"))
				return
			}
			if err != nil {
				return
			}
		}
	}()

	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpLn.Close()

	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		client, err := tcpLn.Accept()
		if err != nil {
			return
		}
		relay(sock, 1234, client)
	}()

	client, err := net.Dial("tcp", tcpLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))

	if _, err := client.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := client.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}

	out, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("read relay output: %v", err)
	}
	if string(out) != "helloEOF" {
		t.Fatalf("relay output = %q, want %q (half-close was not propagated)", out, "helloEOF")
	}

	select {
	case <-managerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("fake manager did not finish")
	}
	select {
	case <-relayDone:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not finish")
	}
}

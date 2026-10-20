package manager

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/nickcis/netpair/internal/protocol"
)

type Manager struct {
	name       string
	socketPath string
	procs      map[string]*managedProc
	nextID     int
	mu         sync.Mutex
	listener   net.Listener
	shutdown   bool
}

type managedProc struct {
	id       string
	cmd      *exec.Cmd
	argv     []string
	exitCode int
	done     chan struct{}
}

// FixResolver bind-mounts resolvPath over /etc/resolv.conf inside this
// process's mount namespace. It runs at manager startup inside the pasta
// namespace: pasta leaves the namespace with the host mount view, where
// /etc/resolv.conf typically points at the 127.0.0.53 systemd-resolved stub
// that has no listener inside the namespace. The mount is private to the
// manager's mount namespace (pasta's child mount tree is a slave), so the
// host is unaffected.
func FixResolver(resolvPath string) error {
	data, err := os.ReadFile(resolvPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", resolvPath, err)
	}

	if cur, err := os.ReadFile("/etc/resolv.conf"); err == nil && string(cur) == string(data) {
		return nil // already correct
	}

	if err := unix.Mount(resolvPath, "/etc/resolv.conf", "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind-mount %s over /etc/resolv.conf: %w", resolvPath, err)
	}
	return nil
}

func Run(name, socketPath string) error {
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return err
	}

	m := &Manager{
		name:       name,
		socketPath: socketPath,
		procs:      make(map[string]*managedProc),
		listener:   ln,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		m.close()
	}()

	log.Printf("netpair-manager %q listening on %s (pid %d)", name, socketPath, os.Getpid())

	for {
		conn, err := ln.Accept()
		if err != nil {
			if m.shutdown {
				return nil
			}
			return err
		}
		go m.handleConn(conn)
	}
}

func (m *Manager) close() {
	m.shutdown = true
	m.mu.Lock()
	for _, p := range m.procs {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	}
	m.mu.Unlock()
	_ = m.listener.Close()
}

func (m *Manager) handleConn(conn net.Conn) {
	defer conn.Close()

	peerUID, err := peerUID(conn)
	if err == nil && peerUID != uint32(os.Getuid()) {
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: "permission denied"})
		return
	}

	br := bufio.NewReader(conn)
	req, err := protocol.ReadRequest(br)
	if err != nil {
		return
	}

	switch req.Op {
	case "ping":
		_ = protocol.WriteResponse(conn, protocol.Response{OK: true})
	case "status":
		_ = protocol.WriteResponse(conn, protocol.Response{OK: true, Version: protocol.Version, PID: os.Getpid()})
	case "shutdown":
		_ = protocol.WriteResponse(conn, protocol.Response{OK: true})
		go m.close()
	case "run":
		m.handleRun(conn, req)
	case "kill":
		m.handleKill(conn, req)
	case "signal":
		m.handleSignal(conn, req)
	case "ps":
		m.handlePs(conn)
	case "exec":
		m.handleExec(conn, br, req)
	case "wait":
		m.handleWait(conn, req)
	case "relay":
		m.handleRelay(conn, req)
	default:
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: "unknown op: " + req.Op})
	}
}

func (m *Manager) handleRun(conn net.Conn, req protocol.Request) {
	if len(req.Argv) == 0 {
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: "argv required"})
		return
	}

	id := m.newProcessID(req.ID)

	cmd := exec.Command(req.Argv[0], req.Argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: err.Error()})
		return
	}

	mp := &managedProc{
		id:   id,
		cmd:  cmd,
		argv: append([]string(nil), req.Argv...),
		done: make(chan struct{}),
	}

	m.mu.Lock()
	m.procs[id] = mp
	m.mu.Unlock()

	go func() {
		mp.exitCode = waitExitCode(cmd)
		close(mp.done)
	}()

	_ = protocol.WriteResponse(conn, protocol.Response{OK: true, ID: id, PID: cmd.Process.Pid})
}

// newProcessID returns req.ID when set, otherwise allocates the next
// "proc-N" identifier.
func (m *Manager) newProcessID(id string) string {
	if id != "" {
		return id
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	return fmt.Sprintf("proc-%d", m.nextID)
}

// waitExitCode waits for cmd and returns its exit code (-1 when it was
// terminated by a signal).
func waitExitCode(cmd *exec.Cmd) int {
	err := cmd.Wait()
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return 1
}

func (m *Manager) handleKill(conn net.Conn, req protocol.Request) {
	m.mu.Lock()
	mp, ok := m.procs[req.ID]
	m.mu.Unlock()
	if !ok {
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: "process not found"})
		return
	}
	select {
	case <-mp.done:
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: fmt.Sprintf("process %q has already exited", req.ID)})
		return
	default:
	}
	if mp.cmd.Process != nil {
		_ = mp.cmd.Process.Kill()
	}
	_ = protocol.WriteResponse(conn, protocol.Response{OK: true})
}

func (m *Manager) handleSignal(conn net.Conn, req protocol.Request) {
	if req.Signal <= 0 {
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: "signal required"})
		return
	}
	m.mu.Lock()
	mp, ok := m.procs[req.ID]
	m.mu.Unlock()
	if !ok {
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: "process not found"})
		return
	}
	select {
	case <-mp.done:
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: fmt.Sprintf("process %q has already exited", req.ID)})
		return
	default:
	}
	if mp.cmd.Process != nil {
		if err := mp.cmd.Process.Signal(syscall.Signal(req.Signal)); err != nil {
			msg := err.Error()
			if errors.Is(err, os.ErrProcessDone) {
				msg = fmt.Sprintf("process %q has already exited", req.ID)
			}
			_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: msg})
			return
		}
	}
	_ = protocol.WriteResponse(conn, protocol.Response{OK: true})
}

func (m *Manager) handlePs(conn net.Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var list []protocol.ProcessInfo
	for _, mp := range m.procs {
		running := true
		exitCode := 0
		select {
		case <-mp.done:
			running = false
			// Read after the receive: close(done) happens after the write.
			exitCode = mp.exitCode
		default:
		}
		pid := 0
		if mp.cmd.Process != nil {
			pid = mp.cmd.Process.Pid
		}
		list = append(list, protocol.ProcessInfo{
			ID:       mp.id,
			PID:      pid,
			Argv:     mp.argv,
			Running:  running,
			ExitCode: exitCode,
		})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].PID < list[j].PID })
	_ = protocol.WriteResponse(conn, protocol.Response{OK: true, Processes: list})
}

// handleExec runs argv in the pair with its stdio attached to the client.
// After the JSON acknowledgement the connection carries a raw byte stream:
// client stdin becomes the process's stdin (half-close = EOF), and the
// process's stdout and stderr are merged into the connection. The client
// reads until EOF (the manager closes the stream after the process was
// reaped) and can then fetch the exit code with a "wait" request on a new
// connection.
func (m *Manager) handleExec(conn net.Conn, br *bufio.Reader, req protocol.Request) {
	if len(req.Argv) == 0 {
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: "argv required"})
		return
	}

	id := m.newProcessID(req.ID)

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: "stdin pipe: " + err.Error()})
		return
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: "stdout pipe: " + err.Error()})
		return
	}

	cmd := exec.Command(req.Argv[0], req.Argv[1:]...)
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stdoutW
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: err.Error()})
		return
	}
	// The child holds its own copies of these fds now.
	_ = stdinR.Close()
	_ = stdoutW.Close()

	mp := &managedProc{
		id:   id,
		cmd:  cmd,
		argv: append([]string(nil), req.Argv...),
		done: make(chan struct{}),
	}
	m.mu.Lock()
	m.procs[id] = mp
	m.mu.Unlock()

	go func() {
		mp.exitCode = waitExitCode(cmd)
		close(mp.done)
	}()

	// Acknowledge before any stream bytes flow.
	if err := protocol.WriteResponse(conn, protocol.Response{OK: true, ID: id, PID: cmd.Process.Pid}); err != nil {
		_ = stdinW.Close()
		_ = stdoutR.Close()
		return
	}

	// stdin: client -> child. Reads go through br: it may already hold
	// bytes the client sent right behind the request.
	go func() {
		defer stdinW.Close()
		_, _ = io.Copy(stdinW, br)
	}()

	// stdout+stderr: child -> client.
	if _, err := io.Copy(conn, stdoutR); err != nil {
		// The client went away mid-stream: stop streaming, but leave the
		// process running (it stays visible in 'netpair ps' and can be
		// stopped with 'netpair kill').
		_ = stdoutR.Close()
		_ = stdinW.Close()
		return
	}
	_ = stdoutR.Close()

	// Output ended. Wait for the child to be reaped so a subsequent
	// "wait" request finds its exit code before the stream closes.
	<-mp.done
}

// handleWait blocks until the process identified by req.ID has exited and
// returns its exit code.
func (m *Manager) handleWait(conn net.Conn, req protocol.Request) {
	m.mu.Lock()
	mp, ok := m.procs[req.ID]
	m.mu.Unlock()
	if !ok {
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: "process not found"})
		return
	}
	<-mp.done
	// exitCode is written before done is closed; the receive synchronises
	// with that write.
	_ = protocol.WriteResponse(conn, protocol.Response{OK: true, ID: mp.id, ExitCode: mp.exitCode})
}

func (m *Manager) handleRelay(conn net.Conn, req protocol.Request) {
	if req.Target == "" {
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: "target required"})
		return
	}

	target, err := net.DialTimeout("tcp", req.Target, 5*time.Second)
	if err != nil {
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: err.Error()})
		return
	}

	_ = protocol.WriteResponse(conn, protocol.Response{OK: true})

	// Upgrade connection to raw relay after JSON ack.
	// Client sends relay request on a dedicated connection per session.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(target, conn)
		_ = target.(*net.TCPConn).CloseWrite()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(conn, target)
	}()
	wg.Wait()
	_ = target.Close()
}

func peerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("not unix conn")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var sysErr error
	err = raw.Control(func(fd uintptr) {
		uid, sysErr = getPeerUID(int(fd))
	})
	if err != nil {
		return 0, err
	}
	if sysErr != nil {
		return 0, sysErr
	}
	return uid, nil
}

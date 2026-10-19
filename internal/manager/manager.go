package manager

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

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
		_ = protocol.WriteResponse(conn, protocol.Response{OK: true, PID: os.Getpid()})
	case "shutdown":
		_ = protocol.WriteResponse(conn, protocol.Response{OK: true})
		go m.close()
	case "run":
		m.handleRun(conn, req)
	case "kill":
		m.handleKill(conn, req)
	case "ps":
		m.handlePs(conn)
	case "exec":
		m.handleExec(conn, req)
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

	id := req.ID
	if id == "" {
		m.mu.Lock()
		m.nextID++
		id = fmt.Sprintf("proc-%d", m.nextID)
		m.mu.Unlock()
	}

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
		err := cmd.Wait()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				mp.exitCode = exitErr.ExitCode()
			} else {
				mp.exitCode = 1
			}
		}
		close(mp.done)
	}()

	_ = protocol.WriteResponse(conn, protocol.Response{OK: true, ID: id, PID: cmd.Process.Pid})
}

func (m *Manager) handleKill(conn net.Conn, req protocol.Request) {
	m.mu.Lock()
	mp, ok := m.procs[req.ID]
	m.mu.Unlock()
	if !ok {
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: "process not found"})
		return
	}
	if mp.cmd.Process != nil {
		_ = mp.cmd.Process.Kill()
	}
	_ = protocol.WriteResponse(conn, protocol.Response{OK: true})
}

func (m *Manager) handlePs(conn net.Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var list []protocol.ProcessInfo
	for _, mp := range m.procs {
		running := true
		select {
		case <-mp.done:
			running = false
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
			ExitCode: mp.exitCode,
		})
	}
	_ = protocol.WriteResponse(conn, protocol.Response{OK: true, Processes: list})
}

func (m *Manager) handleExec(conn net.Conn, req protocol.Request) {
	argv := req.Argv
	if len(argv) == 0 && req.Cmd != "" {
		argv = append([]string{req.Cmd}, req.Args...)
	}
	if len(argv) == 0 {
		_ = protocol.WriteResponse(conn, protocol.Response{OK: false, Error: "argv required"})
		return
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		exitCode := 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		_ = protocol.WriteResponse(conn, protocol.Response{
			OK:       false,
			Error:    err.Error(),
			Output:   string(out),
			ExitCode: exitCode,
		})
		return
	}
	_ = protocol.WriteResponse(conn, protocol.Response{OK: true, Output: string(out)})
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

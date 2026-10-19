package pair

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nickcis/netpair/internal/paths"
	"github.com/nickcis/netpair/internal/protocol"
)

func managerBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	candidate := filepath.Join(dir, "netpair-manager")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	if p, err := exec.LookPath("netpair-manager"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("netpair-manager binary not found alongside %s", exe)
}

func Create(name, hostname string) (*State, error) {
	if err := paths.EnsureBase(); err != nil {
		return nil, err
	}

	if existing, err := Load(name); err == nil && Alive(existing.PastaPID) {
		return nil, fmt.Errorf("pair %q already exists and is running", name)
	}

	sockPath, err := paths.SocketPath(name)
	if err != nil {
		return nil, err
	}
	_ = os.Remove(sockPath)

	mgrBin, err := managerBinary()
	if err != nil {
		return nil, err
	}

	args := []string{
		"--foreground",
		"--config-net",
		"-T", "none",
		"-U", "none",
	}
	if hostname != "" {
		args = append(args, "-H", hostname)
	}
	args = append(args, "--", mgrBin, "--socket", sockPath, "--name", name)

	cmd := exec.Command("pasta", args...)
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start pasta: %w", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			if err := protocol.Ping(sockPath); err == nil {
				ready = true
				break
			}
		}
		if !Alive(cmd.Process.Pid) {
			return nil, fmt.Errorf("pasta exited before manager was ready (check user namespaces and passt installation)")
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !ready {
		if !Alive(cmd.Process.Pid) {
			return nil, fmt.Errorf("pasta exited before manager was ready")
		}
		return nil, fmt.Errorf("manager socket not ready at %s", sockPath)
	}

	resp, err := protocol.Call(sockPath, protocol.Request{Op: "status"})
	managerPID := 0
	if err == nil && resp.OK {
		managerPID = resp.PID
	}

	st := &State{
		Name:       name,
		PastaPID:   cmd.Process.Pid,
		ManagerPID: managerPID,
		Hostname:   hostname,
		CreatedAt:  time.Now(),
		SocketPath: sockPath,
	}
	if err := Save(st); err != nil {
		return nil, err
	}
	return st, nil
}

func Destroy(name string) error {
	st, err := Load(name)
	if err != nil {
		return err
	}

	if st.SocketPath != "" {
		_, _ = protocol.Call(st.SocketPath, protocol.Request{Op: "shutdown"})
	}

	if Alive(st.PastaPID) {
		_ = syscall.Kill(st.PastaPID, syscall.SIGTERM)
		waitDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(waitDeadline) {
			if !Alive(st.PastaPID) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if Alive(st.PastaPID) {
			_ = syscall.Kill(st.PastaPID, syscall.SIGKILL)
		}
	}

	if st.SocketPath != "" {
		_ = os.Remove(st.SocketPath)
	}
	return Remove(name)
}

func Status(name string) (*State, error) {
	st, err := Load(name)
	if err != nil {
		return nil, err
	}
	if !Alive(st.PastaPID) {
		return st, fmt.Errorf("pair %q is not running (pasta pid %d dead)", name, st.PastaPID)
	}
	return st, nil
}

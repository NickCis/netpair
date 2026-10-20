package pair

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/nickcis/netpair/internal/paths"
)

type State struct {
	Name           string    `json:"name"`
	PastaPID       int       `json:"pasta_pid"`
	ManagerPID     int       `json:"manager_pid"`                // inside the pair's PID namespace
	ManagerHostPID int       `json:"manager_host_pid,omitempty"` // on the host, when resolvable
	Hostname       string    `json:"hostname,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	SocketPath     string    `json:"socket_path"`
}

func Load(name string) (*State, error) {
	path, err := paths.StatePath(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("pair %q not found: %w", name, err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func Save(st *State) error {
	if err := paths.EnsureBase(); err != nil {
		return err
	}
	path, err := paths.StatePath(st.Name)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func Remove(name string) error {
	path, err := paths.StatePath(name)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

func List() ([]string, error) {
	base, err := paths.BaseDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) > 5 && name[len(name)-5:] == ".json" {
			names = append(names, name[:len(name)-5])
		}
	}
	return names, nil
}

func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	// A child that exited but was not reaped yet still exists as a zombie,
	// and kill(pid, 0) answers yes for zombies. Read /proc/<pid>/stat to
	// tell them apart so callers fail fast instead of waiting for a
	// process that is already gone.
	state, ok := procState(pid)
	if !ok {
		return false
	}
	return state != 'Z' && state != 'X'
}

func procState(pid int) (byte, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	// comm (field 2) may contain spaces and parentheses; the process state
	// (field 3) always follows the closing parenthesis of comm.
	cp := strings.LastIndexByte(string(data), ')')
	if cp < 0 || cp+2 >= len(data) {
		return 0, false
	}
	return data[cp+2], true
}

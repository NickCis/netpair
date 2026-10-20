package pair

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// HostPIDs maps the in-namespace PIDs reported by the pair manager to the
// corresponding host PIDs. A pair has its own PID namespace, so the numbers
// seen with 'netpair ps' do not exist on the host; the mapping is resolved
// by scanning /proc for processes in the pair's PID namespace and reading
// their NSpid entries (host PID first, in-namespace PID last).
// Unresolvable processes (e.g. already exited) are absent from the map.
func HostPIDs(st *State) map[int]int {
	mgr := managerHostPID(st)
	if mgr <= 0 {
		return nil
	}
	ns, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/pid", mgr))
	if err != nil {
		return nil
	}

	out := make(map[int]int)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if link, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/pid", pid)); err != nil || link != ns {
			continue
		}
		if ids := procNSpids(pid); len(ids) > 0 {
			out[ids[len(ids)-1]] = pid
		}
	}
	return out
}

// managerHostPID finds the host PID of the pair manager from pair state.
func managerHostPID(st *State) int {
	return managerHostPIDOf(st.PastaPID, st.ManagerPID)
}

// managerHostPIDOf finds the host PID of the pair manager. The manager runs
// as a child of pasta, and its in-namespace PID is the one the manager
// reports (the manager is PID 1 of the pair's PID namespace).
func managerHostPIDOf(pastaPID, nsPID int) int {
	kids := childPIDs(pastaPID)
	if len(kids) == 0 {
		return 0
	}
	if nsPID == 0 {
		return kids[0]
	}
	for _, k := range kids {
		if ids := procNSpids(k); len(ids) > 0 && ids[len(ids)-1] == nsPID {
			return k
		}
	}
	return 0
}

// childPIDs returns the host PIDs of the direct children of pid.
func childPIDs(pid int) []int {
	path := fmt.Sprintf("/proc/%d/task/%d/children", pid, pid)
	if data, err := os.ReadFile(path); err == nil {
		var pids []int
		for _, f := range strings.Fields(string(data)) {
			if n, err := strconv.Atoi(f); err == nil {
				pids = append(pids, n)
			}
		}
		if len(pids) > 0 {
			return pids
		}
	}

	// Fallback for kernels without /proc/<pid>/task/<tid>/children.
	var pids []int
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		child, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if ppid, ok := procPPid(child); ok && ppid == pid {
			pids = append(pids, child)
		}
	}
	return pids
}

// procNSpids returns the NSpid entries of a process (host PID first,
// in-namespace PIDs following), or nil if they cannot be read.
func procNSpids(pid int) []int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return nil
	}
	return parseNSpid(string(data))
}

// parseNSpid extracts the NSpid values from /proc/<pid>/status content.
func parseNSpid(status string) []int {
	for _, line := range strings.Split(status, "\n") {
		if !strings.HasPrefix(line, "NSpid:") {
			continue
		}
		var ids []int
		for _, f := range strings.Fields(strings.TrimPrefix(line, "NSpid:")) {
			if n, err := strconv.Atoi(f); err == nil {
				ids = append(ids, n)
			}
		}
		return ids
	}
	return nil
}

// procPPid returns the parent PID from /proc/<pid>/stat.
func procPPid(pid int) (int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	// comm (field 2) may contain spaces and parentheses; parental fields
	// follow the closing parenthesis: state, ppid, ...
	cp := strings.LastIndexByte(string(data), ')')
	if cp < 0 || cp+2 >= len(data) {
		return 0, false
	}
	fields := strings.Fields(string(data[cp+2:]))
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

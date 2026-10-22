package pair

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nickcis/netpair/internal/paths"
	"github.com/nickcis/netpair/internal/protocol"
)

// tailFile returns the last max bytes of a file.
func tailFile(path string, max int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	if len(data) > max {
		data = data[len(data)-max:]
		if i := strings.IndexByte(string(data), '\n'); i >= 0 {
			data = data[i+1:] // start at a line boundary, the first one may be cut
		}
	}
	return string(data), nil
}

// startLogTail tails a log file to w until the returned stop function is
// called. Reading starts at the beginning of the (freshly truncated) file
// and continues as pasta and the manager append. stop is idempotent and
// blocks at most 200 ms; any buffered lines are flushed.
func startLogTail(path string, w io.Writer) (stop func()) {
	f, err := os.Open(path)
	if err != nil {
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		defer f.Close()
		for {
			// Reads hit EOF (0, nil) as soon as the file is drained;
			// data appended afterwards is picked up next round.
			_, _ = io.Copy(w, f)
			select {
			case <-done:
				// Final drain: a line written between the last read
				// and stop() must not be lost.
				_, _ = io.Copy(w, f)
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			select {
			case <-stopped:
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
}

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

func Create(name, hostname string, verbose bool) (*State, error) {
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

	logPath, err := paths.LogPath(name)
	if err != nil {
		return nil, err
	}
	// O_APPEND matters: pasta and the manager inherit this fd (shared file
	// offset, starting at 0), while pasta's own --log-file writer appends.
	// Without O_APPEND the manager's first log lines would overwrite the
	// beginning of pasta's log.
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()

	// In verbose mode mirror the log file to stderr while the pair starts,
	// so pasta's and the manager's output is visible instead of only
	// landing in <name>.log. Children keep the file as their log sink via
	// inherited fds, which keeps working after this CLI process exits —
	// that is why verbose streams the file instead of re-wiring fds.
	stopLogTail := func() {}
	if verbose {
		stopLogTail = startLogTail(logPath, os.Stderr)
	}
	defer stopLogTail()

	resolvPath, err := paths.ResolvPath(name)
	if err != nil {
		return nil, err
	}

	args := []string{
		"--foreground",
		"--config-net",
		// Isolation: forward no ports in either direction. Outbound TCP
		// still works by being spliced through pasta; the host can only
		// reach pair services through explicit 'netpair connect'.
		"-t", "none",
		"-T", "none",
		"-u", "none",
		"-U", "none",
		"--log-file", logPath,
	}
	if hostname != "" {
		args = append(args, "-H", hostname)
	}
	managerArgs := []string{
		mgrBin, "--socket", sockPath, "--name", name,
		// Processes managed by this pair run with the invoking user's real
		// UID/GID (pasta maps that user to root inside the pair namespace).
		"--host-uid", strconv.Itoa(os.Getuid()),
		"--host-gid", strconv.Itoa(os.Getgid()),
	}
	if path := writePairResolvConf(resolvPath); path != "" {
		// pasta --config-net leaves the namespace with a copy of the
		// host's resolv.conf. On systemd hosts that points at the
		// 127.0.0.53 stub, which does not exist inside the namespace,
		// so hand the manager a corrected file to bind-mount instead.
		managerArgs = append(managerArgs, "--resolv", path)
	}
	args = append(args, "--")
	args = append(args, managerArgs...)

	cmd := exec.Command("pasta", args...)
	cmd.Stdin = os.Stdin
	// pasta (and the manager it execs) inherit this fd; keep pasta's own
	// chatter out of the way by pointing stdout at the log as well.
	cmd.Stdout = logFile
	cmd.Stderr = logFile

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
			_ = cmd.Wait() // reap the exited child so it does not linger as a zombie
			return nil, pastaStartupError(logPath)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !ready {
		if !Alive(cmd.Process.Pid) {
			_ = cmd.Wait()
			return nil, pastaStartupError(logPath)
		}
		return nil, fmt.Errorf("manager socket not ready at %s (pasta log: %s)", sockPath, logPath)
	}

	resp, err := protocol.Call(sockPath, protocol.Request{Op: "status"})
	managerPID := 0
	managerHostPID := 0
	if err == nil && resp.OK {
		// resp.PID is the manager's PID inside the pair's PID namespace;
		// resolve the host PID so state and CLI output show a real PID.
		managerPID = resp.PID
		managerHostPID = managerHostPIDOf(cmd.Process.Pid, resp.PID)
	}

	st := &State{
		Name:           name,
		PastaPID:       cmd.Process.Pid,
		ManagerPID:     managerPID,
		ManagerHostPID: managerHostPID,
		Hostname:       hostname,
		CreatedAt:      time.Now(),
		SocketPath:     sockPath,
	}
	if err := Save(st); err != nil {
		return nil, err
	}
	return st, nil
}

// pastaStartupError builds an error for a pasta process that exited before
// the manager was ready. It surfaces the tail of pasta's log so the actual
// failure is visible, and recognises unprivileged user namespace denials.
func pastaStartupError(logPath string) error {
	detail, _ := tailFile(logPath, 8192)

	msg := "pasta exited before manager was ready"
	if s := strings.TrimSpace(detail); s != "" {
		msg += ":\n" + s
	}

	if usernsDenial(detail) {
		msg += "\n\n" + unprivilegedUsernsHint()
	} else {
		msg += fmt.Sprintf("\n\nRun 'netpair doctor' to diagnose (pasta log: %s)", logPath)
	}
	return fmt.Errorf("%s", msg)
}

// usernsDenial reports whether pasta's log indicates that creating a user
// namespace (or mapping uids into it) was denied by the kernel.
func usernsDenial(detail string) bool {
	patterns := []string{
		"Couldn't create user namespace",
		"/proc/self/uid_map",
		"uid_map",
		"Couldn't configure user mappings",
		"clone: Operation not permitted",
		"DENIED",
	}
	for _, p := range patterns {
		if strings.Contains(detail, p) {
			return true
		}
	}
	return false
}

func unprivilegedUsernsHint() string {
	return `pasta was denied while creating its user namespace. Usually this is the
AppArmor restriction on unprivileged user namespaces (Ubuntu 24.04,
kernel.apparmor_restrict_unprivileged_userns) — or a pasta AppArmor profile
that does not itself grant userns creation. Options:

  1) If /etc/apparmor.d/usr.bin.pasta exists, reload it (older netpair
     releases shipped a profile without the required 'userns' rule; the
     profile also decides for pasta even when the sysctl is 0):
       sudo apparmor_parser -r /etc/apparmor.d/usr.bin.pasta
  2) If no profile exists, install netpair's package (it ships and loads
     the profile):
       sudo dpkg -i <netpair .deb>
  3) Or loosen the system-wide restriction (unconfined pasta only; a
     loaded profile still needs 'userns'):
       sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0

Verify with 'netpair doctor'.`
}

// writePairResolvConf generates a corrected resolver configuration for a
// pair. pasta gives the namespace a view of the host's /etc/resolv.conf; on
// systemd hosts that file points at the 127.0.0.53 stub, which has no
// listener inside the namespace. When /etc/resolv.conf uses such stubs, a
// corrected file is generated from systemd-resolved's upstream copy (which
// carries the real nameservers plus search/options) with every nameserver
// entry replaced by the real, non-loopback ones, so that DNS works inside
// the pair namespace.
// Returns the path when a corrected file was written, "" when there is
// nothing to fix (no stubs in /etc/resolv.conf or no useful upstream).
func writePairResolvConf(path string) string {
	realServers := nonLoopbackNameservers()
	if realServers == nil {
		return ""
	}

	pair, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return ""
	}
	if !hasLoopbackNameserver(string(pair)) {
		return "" // the copy pasta will expose is fine as-is
	}

	data := pair
	if upstream, err := os.ReadFile("/run/systemd/resolve/resolv.conf"); err == nil {
		data = upstream
	}

	var body strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "nameserver" {
			continue // all nameserver entries replaced by real servers below
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	for _, s := range realServers {
		body.WriteString("nameserver " + s + "\n")
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		return ""
	}
	return path
}

func hasLoopbackNameserver(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "nameserver" &&
			(strings.HasPrefix(fields[1], "127.") || fields[1] == "::1") {
			return true
		}
	}
	return false
}

// nonLoopbackNameservers returns nameserver addresses from the host's resolv
// configuration, preferring systemd-resolved's upstream file over the stub
// copy in /etc/resolv.conf.
func nonLoopbackNameservers() []string {
	var addrs []string
	seen := make(map[string]bool)
	consider := func(path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) < 2 || fields[0] != "nameserver" {
				continue
			}
			addr := fields[1]
			if strings.HasPrefix(addr, "127.") || addr == "::1" {
				continue
			}
			if !seen[addr] {
				seen[addr] = true
				addrs = append(addrs, addr)
			}
		}
	}
	consider("/run/systemd/resolve/resolv.conf")
	consider("/etc/resolv.conf")
	return addrs
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
	if resolvPath, err := paths.ResolvPath(name); err == nil {
		_ = os.Remove(resolvPath)
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

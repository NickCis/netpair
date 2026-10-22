package pair

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nickcis/netpair/internal/paths"
	"github.com/nickcis/netpair/internal/protocol"
)

// Check is a single doctor diagnostic result.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "ok", "warn" or "fail"
	Detail string `json:"detail,omitempty"`
}

func ok(name, detail string) Check   { return Check{Name: name, Status: "ok", Detail: detail} }
func warn(name, detail string) Check { return Check{Name: name, Status: "warn", Detail: detail} }
func fail(name, detail string) Check { return Check{Name: name, Status: "fail", Detail: detail} }

// Doctor runs environment diagnostics for netpair: pasta presence, the
// manager binary, the runtime directory, AppArmor's unprivileged user
// namespace restriction, and two live pasta probes (one exercising the user
// namespace, one exercising outbound DNS lookups) that mirror how Create
// invokes pasta.
func Doctor() []Check {
	var checks []Check

	// -- pasta binary
	pastaPath, err := exec.LookPath("pasta")
	if err != nil {
		checks = append(checks, fail("pasta binary", "not found in PATH (apt install passt)"))
	} else {
		out, err := exec.Command(pastaPath, "--version").CombinedOutput()
		if err != nil {
			checks = append(checks, fail("pasta binary", fmt.Sprintf("%s: --version failed: %s", pastaPath, strings.TrimSpace(string(out)))))
		} else {
			version := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
			checks = append(checks, ok("pasta binary", fmt.Sprintf("%s (%s)", pastaPath, version)))
		}
	}

	// -- manager binary
	if mgr, err := managerBinary(); err != nil {
		checks = append(checks, fail("netpair-manager binary", err.Error()))
	} else {
		checks = append(checks, ok("netpair-manager binary", mgr))
	}

	// -- runtime dir
	if err := paths.EnsureBase(); err != nil {
		checks = append(checks, fail("runtime dir", err.Error()))
	} else {
		base, _ := paths.BaseDir()
		if env := os.Getenv("XDG_RUNTIME_DIR"); env == "" {
			checks = append(checks, warn("runtime dir", fmt.Sprintf("%s (XDG_RUNTIME_DIR not set, using default)", base)))
		} else {
			checks = append(checks, ok("runtime dir", base))
		}
	}

	restricted, restrictionChecks := apparmorUsernsRestriction()
	checks = append(checks, restrictionChecks...)

	// -- pasta apparmor profile
	profile := "/etc/apparmor.d/usr.bin.pasta"
	if _, err := os.Stat(profile); err != nil {
		if restricted {
			checks = append(checks, fail("pasta apparmor profile", fmt.Sprintf(
				"%s not found; with apparmor_restrict_unprivileged_userns=1 an unconfined pasta cannot create namespaces\n\n%s",
				profile, unprivilegedUsernsHint())))
		} else {
			checks = append(checks, warn("pasta apparmor profile",
				fmt.Sprintf("%s not found (only needed while unprivileged user namespaces are restricted)", profile)))
		}
	} else if grants, err := profileGrantsUserns(profile); err != nil {
		checks = append(checks, warn("pasta apparmor profile", fmt.Sprintf("%s unreadable: %v", profile, err)))
	} else if !grants {
		checks = append(checks, warn("pasta apparmor profile", fmt.Sprintf(
			"%s does not grant userns creation ('userns' rule). A confined pasta then fails with\n"+
				"'Couldn't create user namespace: Permission denied' — even with the restriction sysctls\n"+
				"set to 0. Reload an up-to-date profile with:\n"+
				"  sudo apparmor_parser -r /etc/apparmor.d/usr.bin.pasta", profile)))
	} else {
		checks = append(checks, ok("pasta apparmor profile", profile))
	}

	if restricted {
		// The manager needs its own profile transition to escape the pasta
		// confinement *and* keep the userns permission: an unconfined
		// manager would be forced into the restrictive unprivileged_userns
		// profile when it creates the per-process user namespace.
		if transitions, err := profileTransitionsManager(profile); err == nil && !transitions {
			checks = append(checks, warn("pasta apparmor profile",
				fmt.Sprintf("%s runs /usr/bin/netpair-manager without a profile transition (Ux);\n"+
					"on this system the manager cannot map managed processes to your uid. Reload the current profile:\n"+
					"  sudo apparmor_parser -r /etc/apparmor.d/usr.bin.pasta", profile)))
		}
		mgrProfile := "/etc/apparmor.d/usr.bin.netpair-manager"
		if _, err := os.Stat(mgrProfile); err != nil {
			checks = append(checks, warn("netpair-manager apparmor profile", fmt.Sprintf(
				"%s not found; managed processes cannot switch to your uid on this system\n"+
					"(install an up-to-date netpair package)", mgrProfile)))
		} else if grants, err := profileGrantsUserns(mgrProfile); err != nil {
			checks = append(checks, warn("netpair-manager apparmor profile", fmt.Sprintf("%s unreadable: %v", mgrProfile, err)))
		} else if !grants {
			checks = append(checks, warn("netpair-manager apparmor profile", fmt.Sprintf(
				"%s does not grant userns creation ('userns' rule); managed processes cannot switch to your uid\n"+
					"Reload an up-to-date profile with:\n  sudo apparmor_parser -r %s", mgrProfile, mgrProfile)))
		} else {
			checks = append(checks, ok("netpair-manager apparmor profile", mgrProfile))
		}
	}

	// -- ss for connect auto-discovery
	if ss, err := exec.LookPath("ss"); err != nil {
		checks = append(checks, warn("ss (iproute2)",
			"not found in PATH; 'netpair connect' auto-discovery requires it (apt install iproute2)"))
	} else {
		checks = append(checks, ok("ss (iproute2)", ss))
	}

	// -- existing pairs and their managers
	checks = append(checks, pairChecks()...)

	// -- functional probes through pasta itself
	if pastaPath != "" {
		checks = append(checks, pastaProbe("pasta namespace probe",
			"pasta creates a user + network namespace and keeps a shell running in it"))
		checks = append(checks, pastaProbe("pasta internet probe",
			"DNS inside the pasta namespace resolves example.com"))
	}

	return checks
}

// apparmorUsernsRestriction reads the relevant sysctls and returns whether
// unprivileged user namespaces are restricted for unconfined programs.
func apparmorUsernsRestriction() (restricted bool, checks []Check) {
	readInt := func(path string) (int, bool) {
		data, err := os.ReadFile(path)
		if err != nil {
			return 0, false
		}
		v, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return 0, false
		}
		return v, true
	}

	if v, found := readInt("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); found {
		if v == 1 {
			checks = append(checks, warn("apparmor userns restriction",
				"/proc/sys/kernel/apparmor_restrict_unprivileged_userns = 1"))
			return true, checks
		}
		checks = append(checks, ok("apparmor userns restriction",
			"/proc/sys/kernel/apparmor_restrict_unprivileged_userns = 0"))
		return false, checks
	}
	// Debian's older knob, informational only.
	if v, found := readInt("/proc/sys/kernel/unprivileged_userns_clone"); found && v == 0 {
		checks = append(checks, fail("apparmor userns restriction",
			"/proc/sys/kernel/unprivileged_userns_clone = 0"))
		return true, checks
	}
	checks = append(checks, ok("apparmor userns restriction", "no restrictive sysctl found"))
	return false, checks
}

// pairChecks reports the state of existing pairs. A manager process keeps
// running the binary it was started with, so after a package upgrade pairs
// created earlier may still speak an older protocol or run commands as root,
// and need recreation.
func pairChecks() []Check {
	names, err := List()
	if err != nil || len(names) == 0 {
		return nil
	}
	var checks []Check
	for _, n := range names {
		st, err := Load(n)
		if err != nil {
			checks = append(checks, warn("pair "+n, "state unreadable: "+err.Error()))
			continue
		}
		if !Alive(st.PastaPID) {
			checks = append(checks, warn("pair "+n,
				"not running (stale state; clean up with 'netpair destroy "+n+"')"))
			continue
		}
		resp, err := protocol.Call(st.SocketPath, protocol.Request{Op: "status"})
		if err != nil {
			checks = append(checks, warn("pair "+n, "manager not reachable: "+err.Error()))
			continue
		}
		if !resp.OK {
			checks = append(checks, warn("pair "+n, "manager error: "+resp.Error))
			continue
		}
		if resp.Version != protocol.Version {
			checks = append(checks, warn("pair "+n, fmt.Sprintf(
				"manager speaks protocol %d, this netpair speaks %d; recreate the pair to use the new binary:\n"+
					"  netpair destroy %s && netpair create %s", resp.Version, protocol.Version, n, n)))
			continue
		}
		if resp.RunAsUID != os.Getuid() {
			checks = append(checks, warn("pair "+n, fmt.Sprintf(
				"manager runs commands as uid %d, want %d (pair created by an older netpair); recreate it:\n"+
					"  netpair destroy %s && netpair create %s", resp.RunAsUID, os.Getuid(), n, n)))
			continue
		}
		checks = append(checks, ok("pair "+n, fmt.Sprintf(
			"running, manager protocol %d, commands run as uid %d", resp.Version, resp.RunAsUID)))
	}
	return checks
}

// profileTransitionsManager reports whether the pasta profile execs
// /usr/bin/netpair-manager with a profile transition (px/cx) instead of
// unconfined (Ux). Without the transition the manager is unconfined and,
// on systems restricting unprivileged user namespaces, cannot create the
// per-process user namespace used to map commands to the caller's uid.
func profileTransitionsManager(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if !strings.Contains(line, "/usr/bin/netpair-manager") {
			continue
		}
		for _, f := range strings.Fields(line) {
			if strings.HasPrefix(f, "px") || strings.HasPrefix(f, "cx") {
				return true, nil
			}
		}
	}
	return false, nil
}

// profileGrantsUserns reports whether an AppArmor profile text contains a
// 'userns' allow rule. Profiles without that rule deny user namespace
// creation on kernels enforcing the AppArmor userns restriction — even
// when the restriction sysctls are 0, because the profile decides for
// confined programs.
func profileGrantsUserns(profile string) (bool, error) {
	data, err := os.ReadFile(profile)
	if err != nil {
		return false, err
	}
	// Match a bare "userns," rule (+ optional trailing comment), the forms
	// used by pasta and other userns-creating programs (podman, chrome, ...).
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if strings.TrimSpace(line) == "userns," {
			return true, nil
		}
	}
	return false, nil
}

// pastaProbe runs pasta with the same flags Create uses, execing a command in
// the namespace, and reports pasta's log tail on failure.
func pastaProbe(name, describe string) Check {
	if err := paths.EnsureBase(); err != nil {
		return fail(name, "cannot create runtime dir: "+err.Error())
	}
	logPath, err := paths.LogPath("doctor-probe")
	if err != nil {
		return fail(name, err.Error())
	}
	_ = os.Remove(logPath)
	defer func() { _ = os.Remove(logPath) }()

	resolvPath, err := paths.ResolvPath("doctor-probe")
	if err != nil {
		return fail(name, err.Error())
	}
	_ = os.Remove(resolvPath)
	resolv := writePairResolvConf(resolvPath)
	if strings.Contains(name, "internet") && resolv == "" {
		return fail(name, "no usable upstream DNS servers found on host; cannot probe internet connectivity")
	}
	defer func() { _ = os.Remove(resolvPath) }()

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fail(name, err.Error())
	}
	defer func() { _ = logFile.Close() }()

	args := []string{
		"--foreground",
		"--config-net",
		"-t", "none",
		"-T", "none",
		"-u", "none",
		"-U", "none",
		"--log-file", logPath,
	}
	if strings.Contains(name, "internet") {
		// Mirror what Create does: the manager would bind-mount the
		// corrected resolv.conf; the probe does the same inline.
		args = append(args, "--", "/bin/sh", "-c",
			"mount --bind "+resolv+" /etc/resolv.conf && getent hosts example.com >/dev/null")
	} else {
		args = append(args, "--", "/bin/sh", "-c", "true")
	}

	cmd := exec.Command("pasta", args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return fail(name, fmt.Sprintf("start pasta: %v", err))
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return ok(name, describe)
		}
		detail, _ := tailFile(logPath, 4096)
		msg := fmt.Sprintf("pasta exited: %v", err)
		if s := strings.TrimSpace(detail); s != "" {
			msg += "\n" + s
		}
		if usernsDenial(detail) {
			msg += "\n\n" + unprivilegedUsernsHint()
		}
		return fail(name, msg)
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return fail(name, "pasta probe timed out after 20s")
	}
}

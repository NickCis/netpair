package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/nickcis/netpair/internal/connect"
	"github.com/nickcis/netpair/internal/pair"
	"github.com/nickcis/netpair/internal/protocol"
	"github.com/nickcis/netpair/internal/sshclient"
)

func main() {
	args := os.Args[1:]
	verbose := os.Getenv("NETPAIR_VERBOSE") != "" && os.Getenv("NETPAIR_VERBOSE") != "0"
	// --verbose/-v is a global flag: it may appear anywhere before the
	// first "--" separator; anything after "--" is passed to the pair
	// process verbatim.
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--verbose", "-v":
			verbose = true
		case "--":
			filtered = append(filtered, args[i:]...)
			i = len(args)
		default:
			filtered = append(filtered, args[i])
		}
	}
	if len(filtered) == 0 {
		usage()
		os.Exit(2)
	}

	cmd := filtered[0]
	rest := filtered[1:]

	switch cmd {
	case "create":
		runCreate(rest, verbose)
	case "destroy":
		runDestroy(rest)
	case "list":
		runList()
	case "doctor":
		runDoctor()
	case "status":
		runStatus(rest)
	case "run":
		runExec(rest)
	case "kill":
		runKill(rest)
	case "ps":
		runPs(rest)
	case "connect":
		runConnect(rest)
	case "ssh-tunnel":
		runSSHTunnel(rest)
	case "ssh-remote":
		runSSHRemote(rest)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `netpair — isolated network namespace pairs

Usage:
  netpair [--verbose] <command> [args...]

Commands:
  create <name> [--hostname NAME]
  destroy <name>
  list
  doctor             Check the environment (pasta, user namespaces, DNS, ...)
  status <name>      Show pair state (JSON)
  run <name> [--daemon] -- <argv...>
                     Run a process in the pair. By default stdio is
                     attached and netpair waits for its exit code;
                     --daemon detaches it and logs its output to the
                     pair log instead.
  kill <name> --id <proc-id> [--signal SIG]
                     Terminate a managed process (default SIGKILL; SIG is
                     a name like TERM or a number).
  ps <name>          List managed processes with in-pair and host PIDs
  connect <name> [--port N] [--port N:HOST_N]
  ssh-tunnel <name> --remote user@host [-p PORT] --bind-name NAME --bind-port PORT [--local-host HOST] [--local-port PORT]
  ssh-remote --remote user@host [-p PORT] --bind-name NAME --bind-port PORT [--local-port PORT] [-- <argv...>]

Flags:
  --verbose, -v    Stream the pair's log to stderr while it starts (create);
                   also NETPAIR_VERBOSE=1. Only valid before a "--" separator.
  --daemon         (run) Detach instead of attaching stdio; the process keeps
                   running and its output goes to the pair log.

Requirements: passt (pasta)
`)
}

func requireName(args []string) (string, []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "pair name required")
		os.Exit(2)
	}
	return args[0], args[1:]
}

func runCreate(args []string, verbose bool) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: netpair create <name> [--hostname NAME]")
		os.Exit(2)
	}
	name := args[0]
	hostname := ""
	for i := 1; i < len(args); i++ {
		if args[i] == "--hostname" && i+1 < len(args) {
			hostname = args[i+1]
			i++
		}
	}
	st, err := pair.Create(name, hostname, verbose)
	if err != nil {
		die(err)
	}
	mgr := st.ManagerPID
	if st.ManagerHostPID > 0 {
		mgr = st.ManagerHostPID
	}
	fmt.Printf("created pair %q (pasta pid %d, manager pid %d)\n", st.Name, st.PastaPID, mgr)
}

func runDestroy(args []string) {
	name, _ := requireName(args)
	if err := pair.Destroy(name); err != nil {
		die(err)
	}
	fmt.Printf("destroyed pair %q\n", name)
}

func runList() {
	names, err := pair.List()
	if err != nil {
		die(err)
	}
	if len(names) == 0 {
		fmt.Println("no pairs")
		return
	}
	for _, n := range names {
		st, err := pair.Load(n)
		if err != nil {
			fmt.Fprintf(os.Stderr, "netpair: skipping pair %s: %v\n", n, err)
			continue
		}
		status := "running"
		if !pair.Alive(st.PastaPID) {
			status = "dead"
		}
		fmt.Printf("%s\t%s\tpasta=%d\n", n, status, st.PastaPID)
	}
}

func runDoctor() {
	checks := pair.Doctor()
	exit := 0
	for _, c := range checks {
		switch c.Status {
		case "ok":
			fmt.Printf("[+] %s: %s\n", c.Name, c.Detail)
		case "warn":
			fmt.Printf("[!] %s: %s\n", c.Name, c.Detail)
		case "fail":
			exit = 1
			fmt.Printf("[-] %s: %s\n", c.Name, c.Detail)
		}
	}
	if exit != 0 {
		os.Exit(exit)
	}
}

func runStatus(args []string) {
	name, _ := requireName(args)
	st, err := pair.Status(name)
	if err != nil {
		die(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(st)
}

func splitArgs(args []string) (flags []string, cmd []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

func socketFor(name string) string {
	st, err := pair.Load(name)
	if err != nil {
		die(err)
	}
	return st.SocketPath
}

func runExec(args []string) {
	name, rest := requireName(args)
	flags, cmd := splitArgs(rest)
	if len(cmd) == 0 {
		fmt.Fprintln(os.Stderr, "usage: netpair run <name> [--daemon] -- <argv...>")
		os.Exit(2)
	}
	daemon := false
	for _, f := range flags {
		switch f {
		case "--daemon":
			daemon = true
		default:
			fmt.Fprintf(os.Stderr, "netpair: unknown flag for run: %s\n", f)
			fmt.Fprintln(os.Stderr, "usage: netpair run <name> [--daemon] -- <argv...>")
			os.Exit(2)
		}
	}

	sock := socketFor(name)
	if daemon {
		resp, err := protocol.Call(sock, protocol.Request{Op: "run", Argv: cmd})
		if err != nil {
			die(err)
		}
		if !resp.OK {
			die(fmt.Errorf("%s", resp.Error))
		}
		fmt.Printf("started %q pid %d\n", resp.ID, resp.PID)
		return
	}

	// Attached runs use the streaming exec/wait protocol, which older
	// managers do not implement.
	if err := protocol.CheckVersion(name, sock); err != nil {
		die(err)
	}

	code, err := runStreamed(sock, cmd)
	if err != nil {
		die(err)
	}
	os.Exit(code)
}

func runKill(args []string) {
	name, rest := requireName(args)
	id := ""
	signal := 0
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--id":
			if i+1 < len(rest) {
				id = rest[i+1]
				i++
			}
		case "--signal":
			if i+1 >= len(rest) {
				fmt.Fprintln(os.Stderr, "usage: netpair kill <name> --id <proc-id> [--signal SIG]")
				os.Exit(2)
			}
			sig, err := parseSignal(rest[i+1])
			if err != nil {
				die(err)
			}
			signal = sig
			i++
		}
	}
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: netpair kill <name> --id <proc-id> [--signal SIG]")
		os.Exit(2)
	}
	op := "kill"
	if signal != 0 {
		op = "signal"
	}
	resp, err := protocol.Call(socketFor(name), protocol.Request{Op: op, ID: id, Signal: signal})
	if err != nil {
		die(err)
	}
	if !resp.OK {
		if signal != 0 && strings.Contains(resp.Error, "unknown op") {
			die(fmt.Errorf("pair %q is managed by an older netpair-manager that does not support --signal;\nrecreate the pair first:\n  netpair destroy %s && netpair create %s", name, name, name))
		}
		die(fmt.Errorf("%s", resp.Error))
	}
	fmt.Printf("killed %q\n", id)
}

// parseSignal accepts a signal name (TERM, KILL, INT, ...) or a number.
func parseSignal(s string) (int, error) {
	names := map[string]syscall.Signal{
		"HUP":  syscall.SIGHUP,
		"INT":  syscall.SIGINT,
		"QUIT": syscall.SIGQUIT,
		"KILL": syscall.SIGKILL,
		"TERM": syscall.SIGTERM,
		"USR1": syscall.SIGUSR1,
		"USR2": syscall.SIGUSR2,
		"STOP": syscall.SIGSTOP,
		"CONT": syscall.SIGCONT,
	}
	if sig, ok := names[strings.ToUpper(strings.TrimPrefix(s, "SIG"))]; ok {
		return int(sig), nil
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 && n < 65 {
		return n, nil
	}
	return 0, fmt.Errorf("unknown signal %q (use a name like TERM or a number)", s)
}

func runPs(args []string) {
	name, _ := requireName(args)
	st, err := pair.Load(name)
	if err != nil {
		die(err)
	}
	resp, err := protocol.Call(st.SocketPath, protocol.Request{Op: "ps"})
	if err != nil {
		die(err)
	}
	if !resp.OK {
		die(fmt.Errorf("%s", resp.Error))
	}
	if len(resp.Processes) == 0 {
		fmt.Println("no processes")
		return
	}

	hostPIDs := pair.HostPIDs(st)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNS-PID\tHOST-PID\tSTATUS\tCOMMAND")
	for _, p := range resp.Processes {
		host := "-"
		if h, ok := hostPIDs[p.PID]; ok {
			host = strconv.Itoa(h)
		}
		status := "running"
		if !p.Running {
			status = fmt.Sprintf("exited(%d)", p.ExitCode)
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n", p.ID, p.PID, host, status, strings.Join(p.Argv, " "))
	}
	_ = w.Flush()
}

func runConnect(args []string) {
	name, rest := requireName(args)
	var maps []connect.PortMap
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--port" && i+1 < len(rest) {
			m, err := connect.ParsePortSpec(rest[i+1])
			if err != nil {
				die(err)
			}
			maps = append(maps, m)
			i++
		}
	}
	if err := connect.Run(name, maps); err != nil {
		die(err)
	}
}

func runSSHTunnel(args []string) {
	name, rest := requireName(args)
	fs := parseFlags(rest)

	remote := fs.get("remote")
	bindName := fs.get("bind-name")
	bindPort := fs.getInt("bind-port", 0)
	localHost := fs.get("local-host")
	if localHost == "" {
		localHost = "127.0.0.1"
	}
	localPort := fs.getInt("local-port", bindPort)
	sshPort := fs.getInt("p", 22)
	insecure := fs.has("insecure-host-key")
	identity := fs.get("identity")

	if remote == "" || bindName == "" || bindPort == 0 {
		fmt.Fprintln(os.Stderr, "usage: netpair ssh-tunnel <name> --remote user@host [-p PORT] --bind-name NAME --bind-port PORT [--local-host HOST] [--local-port PORT]")
		os.Exit(2)
	}

	user, host, err := sshclient.ParseRemote(remote)
	if err != nil {
		die(err)
	}

	client, err := sshclient.Dial(sshclient.Config{
		User:            user,
		Host:            host,
		Port:            sshPort,
		IdentityFile:    identity,
		InsecureHostKey: insecure,
	})
	if err != nil {
		die(err)
	}
	defer client.Close()

	if err := sshclient.ReverseTunnel(client, bindName, bindPort, localHost, localPort); err != nil {
		die(err)
	}
	_ = name // pair name kept for CLI consistency; tunnel targets host-local ports from connect
}

func runSSHRemote(args []string) {
	flags, cmd := splitArgs(args)
	fs := parseFlags(flags)

	remote := fs.get("remote")
	bindName := fs.get("bind-name")
	bindPort := fs.getInt("bind-port", 0)
	localPort := fs.getInt("local-port", bindPort)
	sshPort := fs.getInt("p", 22)
	insecure := fs.has("insecure-host-key")
	identity := fs.get("identity")

	if remote == "" || bindName == "" || bindPort == 0 {
		fmt.Fprintln(os.Stderr, "usage: netpair ssh-remote --remote user@host [-p PORT] --bind-name NAME --bind-port PORT [--local-port PORT] [-- <argv...>]")
		os.Exit(2)
	}

	user, host, err := sshclient.ParseRemote(remote)
	if err != nil {
		die(err)
	}

	client, err := sshclient.Dial(sshclient.Config{
		User:            user,
		Host:            host,
		Port:            sshPort,
		IdentityFile:    identity,
		InsecureHostKey: insecure,
	})
	if err != nil {
		die(err)
	}
	defer client.Close()

	if err := sshclient.LocalTunnel(client, bindName, bindPort, localPort, cmd); err != nil {
		die(err)
	}
}

type flagSet map[string]string

func parseFlags(args []string) flagSet {
	fs := make(flagSet)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			continue
		}
		key := strings.TrimLeft(a, "-")
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			fs[key] = args[i+1]
			i++
		} else {
			fs[key] = "true"
		}
	}
	return fs
}

func (f flagSet) get(key string) string { return f[key] }

func (f flagSet) has(key string) bool { _, ok := f[key]; return ok }

func (f flagSet) getInt(key string, def int) int {
	v, ok := f[key]
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		die(fmt.Errorf("invalid integer for --%s: %s", key, v))
	}
	return n
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "netpair: %v\n", err)
	os.Exit(1)
}

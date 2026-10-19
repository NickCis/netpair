package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/nickcis/netpair/internal/connect"
	"github.com/nickcis/netpair/internal/pair"
	"github.com/nickcis/netpair/internal/protocol"
	"github.com/nickcis/netpair/internal/sshclient"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "create":
		runCreate(args)
	case "destroy":
		runDestroy(args)
	case "list":
		runList()
	case "status":
		runStatus(args)
	case "run":
		runExec(args)
	case "kill":
		runKill(args)
	case "ps":
		runPs(args)
	case "connect":
		runConnect(args)
	case "ssh-tunnel":
		runSSHTunnel(args)
	case "ssh-remote":
		runSSHRemote(args)
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
  netpair create <name> [--hostname NAME]
  netpair destroy <name>
  netpair list
  netpair status <name>
  netpair run <name> -- <argv...>
  netpair kill <name> --id <proc-id>
  netpair ps <name>
  netpair connect <name> [--port N] [--port N:HOST_N]
  netpair ssh-tunnel <name> --remote user@host [-p PORT] --bind-name NAME --bind-port PORT [--local-host HOST] [--local-port PORT]
  netpair ssh-remote --remote user@host [-p PORT] --bind-name NAME --bind-port PORT [--local-port PORT] [-- <argv...>]

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

func runCreate(args []string) {
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
	st, err := pair.Create(name, hostname)
	if err != nil {
		die(err)
	}
	fmt.Printf("created pair %q (pasta pid %d, manager pid %d)\n", st.Name, st.PastaPID, st.ManagerPID)
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
			continue
		}
		status := "running"
		if !pair.Alive(st.PastaPID) {
			status = "dead"
		}
		fmt.Printf("%s\t%s\tpasta=%d\n", n, status, st.PastaPID)
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
	_, cmd := splitArgs(rest)
	if len(cmd) == 0 {
		fmt.Fprintln(os.Stderr, "usage: netpair run <name> -- <argv...>")
		os.Exit(2)
	}
	resp, err := protocol.Call(socketFor(name), protocol.Request{Op: "run", Argv: cmd})
	if err != nil {
		die(err)
	}
	if !resp.OK {
		die(fmt.Errorf("%s", resp.Error))
	}
	fmt.Printf("started %q pid %d\n", resp.ID, resp.PID)
}

func runKill(args []string) {
	name, rest := requireName(args)
	id := ""
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--id" && i+1 < len(rest) {
			id = rest[i+1]
		}
	}
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: netpair kill <name> --id <proc-id>")
		os.Exit(2)
	}
	resp, err := protocol.Call(socketFor(name), protocol.Request{Op: "kill", ID: id})
	if err != nil {
		die(err)
	}
	if !resp.OK {
		die(fmt.Errorf("%s", resp.Error))
	}
	fmt.Printf("killed %q\n", id)
}

func runPs(args []string) {
	name, _ := requireName(args)
	resp, err := protocol.Call(socketFor(name), protocol.Request{Op: "ps"})
	if err != nil {
		die(err)
	}
	if !resp.OK {
		die(fmt.Errorf("%s", resp.Error))
	}
	for _, p := range resp.Processes {
		status := "running"
		if !p.Running {
			status = fmt.Sprintf("exited(%d)", p.ExitCode)
		}
		fmt.Printf("%s\tpid=%d\t%s\t%s\n", p.ID, p.PID, status, strings.Join(p.Argv, " "))
	}
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

package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/nickcis/netpair/internal/manager"
)

func main() {
	socket := flag.String("socket", "", "unix socket path")
	name := flag.String("name", "", "pair name")
	resolv := flag.String("resolv", "", "resolv.conf to bind-mount over /etc/resolv.conf inside the namespace")
	hostUID := flag.Int("host-uid", -1, "UID managed processes run as (host view)")
	hostGID := flag.Int("host-gid", -1, "GID managed processes run as (host view)")
	flag.Parse()

	if *socket == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "usage: netpair-manager --socket PATH --name NAME [--resolv PATH] [--host-uid UID --host-gid GID]")
		os.Exit(2)
	}

	if *resolv != "" {
		if err := manager.FixResolver(*resolv); err != nil {
			log.Printf("resolver fixup failed: %v", err)
		}
	}

	if err := manager.Run(*name, *socket, *hostUID, *hostGID); err != nil {
		fmt.Fprintf(os.Stderr, "netpair-manager: %v\n", err)
		os.Exit(1)
	}
}

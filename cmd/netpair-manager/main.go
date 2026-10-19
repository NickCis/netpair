package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/nickcis/netpair/internal/manager"
)

func main() {
	socket := flag.String("socket", "", "unix socket path")
	name := flag.String("name", "", "pair name")
	flag.Parse()

	if *socket == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "usage: netpair-manager --socket PATH --name NAME")
		os.Exit(2)
	}

	if err := manager.Run(*name, *socket); err != nil {
		fmt.Fprintf(os.Stderr, "netpair-manager: %v\n", err)
		os.Exit(1)
	}
}

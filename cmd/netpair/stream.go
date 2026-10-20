package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/nickcis/netpair/internal/protocol"
)

// runStreamed runs argv in the pair with the local terminal attached: stdin
// is forwarded to the process, its stdout and stderr (merged, in order) are
// copied to os.Stdout, and the returned code is the process's exit code.
// SIGINT/SIGTERM terminate the process (a second signal exits immediately);
// when interrupted the return code is 130.
func runStreamed(socketPath string, argv []string) (int, error) {
	conn, br, resp, err := protocol.Dial(socketPath, protocol.Request{Op: "exec", Argv: argv})
	if err != nil {
		return 1, err
	}
	defer conn.Close()
	if !resp.OK {
		return 1, errors.New(resp.Error)
	}
	id := resp.ID

	var interrupted atomic.Bool
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		interrupted.Store(true)
		// Ask the manager to terminate the process, then keep reading
		// until the stream drains so buffered output is not lost.
		_, _ = protocol.Call(socketPath, protocol.Request{Op: "kill", ID: id})
		<-sigCh
		os.Exit(130)
	}()

	outDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(os.Stdout, br)
		outDone <- err
	}()

	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		// Half-close so the process sees EOF on stdin while the output
		// direction stays open.
		if uw, ok := conn.(*net.UnixConn); ok {
			_ = uw.CloseWrite()
		}
	}()

	if outErr := <-outDone; outErr != nil {
		// Local stdout is gone (e.g. the reader closed a pipe). Stop the
		// process instead of leaving it writing into a dead stream.
		_, _ = protocol.Call(socketPath, protocol.Request{Op: "kill", ID: id})
		if errors.Is(outErr, syscall.EPIPE) {
			return 141, nil // 128 + SIGPIPE, conventional silent exit
		}
		return 1, fmt.Errorf("copy output: %w", outErr)
	}

	// The stream only ends after the manager reaped the process, so wait
	// returns the exit code immediately.
	resp, err = protocol.Call(socketPath, protocol.Request{Op: "wait", ID: id})
	if err != nil {
		return 1, err
	}
	if !resp.OK {
		return 1, errors.New(resp.Error)
	}
	if interrupted.Load() {
		return 130, nil
	}
	return resp.ExitCode, nil
}

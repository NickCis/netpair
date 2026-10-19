package sshclient

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/crypto/ssh"
)

func ReverseTunnel(client *ssh.Client, bindName string, bindPort int, localHost string, localPort int) error {
	if localHost == "" {
		localHost = "127.0.0.1"
	}
	if localPort == 0 {
		localPort = bindPort
	}

	remoteAddr := fmt.Sprintf("%s:%d", bindName, bindPort)
	ln, err := client.Listen("tcp", remoteAddr)
	if err != nil {
		return fmt.Errorf("remote listen %s: %w", remoteAddr, err)
	}
	defer ln.Close()

	localAddr := fmt.Sprintf("%s:%d", localHost, localPort)
	fmt.Fprintf(os.Stderr, "Remote forward %s -> %s\n", remoteAddr, localAddr)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go bridgeToLocal(conn, localAddr)
		}
	}()

	waitForSignal()
	return nil
}

func LocalTunnel(client *ssh.Client, bindName string, bindPort, localPort int, runCmd []string) error {
	if localPort == 0 {
		localPort = bindPort
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		return fmt.Errorf("listen 127.0.0.1:%d: %w", localPort, err)
	}
	defer ln.Close()

	remoteAddr := fmt.Sprintf("%s:%d", bindName, bindPort)
	fmt.Fprintf(os.Stderr, "Local forward 127.0.0.1:%d -> %s\n", localPort, remoteAddr)

	go func() {
		for {
			local, err := ln.Accept()
			if err != nil {
				return
			}
			go bridgeToRemote(client, remoteAddr, local)
		}
	}()

	if len(runCmd) > 0 {
		cmd := exec.Command(runCmd[0], runCmd[1:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = os.Environ()
		if err := cmd.Run(); err != nil {
			return err
		}
	}

	waitForSignal()
	return nil
}

func bridgeToLocal(remote net.Conn, localAddr string) {
	defer remote.Close()
	local, err := net.Dial("tcp", localAddr)
	if err != nil {
		return
	}
	defer local.Close()
	copyBoth(remote, local)
}

func bridgeToRemote(client *ssh.Client, remoteAddr string, local net.Conn) {
	defer local.Close()
	remote, err := client.Dial("tcp", remoteAddr)
	if err != nil {
		return
	}
	defer remote.Close()
	copyBoth(local, remote)
}

func copyBoth(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	<-done
}

func waitForSignal() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}

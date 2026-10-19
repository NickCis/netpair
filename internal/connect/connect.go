package connect

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nickcis/netpair/internal/pair"
	"github.com/nickcis/netpair/internal/protocol"
)

type PortMap struct {
	NSPort   int
	HostPort int
}

func Run(name string, maps []PortMap) error {
	st, err := pair.Load(name)
	if err != nil {
		return err
	}

	if len(maps) == 0 {
		maps, err = discoverPorts(st.SocketPath)
		if err != nil {
			return err
		}
	}

	if len(maps) == 0 {
		return fmt.Errorf("no ports to forward")
	}

	type listener struct {
		ln     net.Listener
		nsPort int
	}
	var listeners []listener

	for _, m := range maps {
		hostPort := m.HostPort
		if hostPort == 0 {
			hostPort = m.NSPort
		}
		addr := fmt.Sprintf("127.0.0.1:%d", hostPort)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Printf("warn: host port %d in use, skipping ns:%d -> host:%d", hostPort, m.NSPort, hostPort)
			continue
		}
		log.Printf("forwarding host %s -> ns 127.0.0.1:%d", addr, m.NSPort)
		listeners = append(listeners, listener{ln: ln, nsPort: m.NSPort})
	}

	if len(listeners) == 0 {
		return fmt.Errorf("no host ports could be bound")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var wg sync.WaitGroup
	done := make(chan struct{})

	for _, l := range listeners {
		wg.Add(1)
		go func(l listener) {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				l.ln.(*net.TCPListener).SetDeadline(time.Now().Add(500 * time.Millisecond))
				conn, err := l.ln.Accept()
				if err != nil {
					if ne, ok := err.(net.Error); ok && ne.Timeout() {
						continue
					}
					select {
					case <-done:
						return
					default:
					}
					if err != io.EOF {
						log.Printf("accept error: %v", err)
					}
					return
				}
				go relay(st.SocketPath, l.nsPort, conn)
			}
		}(l)
	}

	<-sigCh
	close(done)
	for _, l := range listeners {
		_ = l.ln.Close()
	}
	wg.Wait()
	return nil
}

func relay(socketPath string, nsPort int, client net.Conn) {
	defer client.Close()

	conn, err := protocol.DialSocket(socketPath)
	if err != nil {
		log.Printf("relay dial manager: %v", err)
		return
	}
	defer conn.Close()

	target := fmt.Sprintf("127.0.0.1:%d", nsPort)
	if err := protocol.WriteRequest(conn, protocol.Request{
		Op:     "relay",
		Target: target,
	}); err != nil {
		log.Printf("relay request: %v", err)
		return
	}

	resp, err := protocol.ReadResponse(bufio.NewReader(conn))
	if err != nil || !resp.OK {
		log.Printf("relay rejected: %v %v", err, resp.Error)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, conn)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(conn, client)
	}()
	wg.Wait()
}

func discoverPorts(socketPath string) ([]PortMap, error) {
	resp, err := protocol.Call(socketPath, protocol.Request{
		Op:  "exec",
		Argv: []string{"ss", "-tlnH"},
	})
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("ss failed: %s", resp.Error)
	}
	return parseSSOutput(resp.Output), nil
}

func parseSSOutput(output string) []PortMap {
	var maps []PortMap
	seen := make(map[int]bool)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		local := fields[3]
		if strings.HasPrefix(local, "[") {
			// IPv6 [::]:port or [::1]:port
			if !strings.Contains(local, "::1") && !strings.Contains(local, "[::1]") {
				continue
			}
			idx := strings.LastIndex(local, ":")
			if idx < 0 {
				continue
			}
			portStr := strings.TrimSuffix(local[idx+1:], "]")
			port, err := strconv.Atoi(portStr)
			if err != nil || seen[port] {
				continue
			}
			seen[port] = true
			maps = append(maps, PortMap{NSPort: port})
			continue
		}
		host, portStr, ok := strings.Cut(local, ":")
		if !ok {
			continue
		}
		if host != "127.0.0.1" {
			continue
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || seen[port] {
			continue
		}
		seen[port] = true
		maps = append(maps, PortMap{NSPort: port})
	}
	return maps
}

func ParsePortSpec(spec string) (PortMap, error) {
	if strings.Contains(spec, ":") {
		parts := strings.SplitN(spec, ":", 2)
		ns, err := strconv.Atoi(parts[0])
		if err != nil {
			return PortMap{}, fmt.Errorf("invalid ns port: %s", parts[0])
		}
		host, err := strconv.Atoi(parts[1])
		if err != nil {
			return PortMap{}, fmt.Errorf("invalid host port: %s", parts[1])
		}
		return PortMap{NSPort: ns, HostPort: host}, nil
	}
	p, err := strconv.Atoi(spec)
	if err != nil {
		return PortMap{}, fmt.Errorf("invalid port: %s", spec)
	}
	return PortMap{NSPort: p}, nil
}

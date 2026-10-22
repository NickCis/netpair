package protocol

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

const DefaultTimeout = 30 * time.Second

// Version identifies the manager wire protocol. A running manager keeps the
// protocol it was started with (package upgrades do not restart it), so
// clients compare it before using ops newer managers implement.
const Version = 2

type Request struct {
	Op      string   `json:"op"`
	Argv    []string `json:"argv,omitempty"`
	ID      string   `json:"id,omitempty"`
	Signal  int      `json:"signal,omitempty"`
	Target  string   `json:"target,omitempty"`
	Session string   `json:"session,omitempty"`
}

type ProcessInfo struct {
	ID       string   `json:"id"`
	PID      int      `json:"pid"`
	Argv     []string `json:"argv"`
	Running  bool     `json:"running"`
	ExitCode int      `json:"exit_code,omitempty"`
}

type Response struct {
	OK        bool          `json:"ok"`
	Error     string        `json:"error,omitempty"`
	Version   int           `json:"version,omitempty"`
	RunAsUID  int           `json:"run_as_uid,omitempty"`
	ID        string        `json:"id,omitempty"`
	PID       int           `json:"pid,omitempty"`
	Output    string        `json:"output,omitempty"`
	ExitCode  int           `json:"exit_code,omitempty"`
	Processes []ProcessInfo `json:"processes,omitempty"`
}

func WriteRequest(w io.Writer, req Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

func ReadRequest(r *bufio.Reader) (Request, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return Request{}, err
	}
	var req Request
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		return Request{}, err
	}
	return req, nil
}

func WriteResponse(w io.Writer, resp Response) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

func ReadResponse(r *bufio.Reader) (Response, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

func DialSocket(path string) (net.Conn, error) {
	return net.DialTimeout("unix", path, DefaultTimeout)
}

func Call(path string, req Request) (Response, error) {
	conn, err := DialSocket(path)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(DefaultTimeout)); err != nil {
		return Response{}, err
	}

	if err := WriteRequest(conn, req); err != nil {
		return Response{}, err
	}

	return ReadResponse(bufio.NewReader(conn))
}

// Dial sends req and reads the response, and hands the connection back to
// the caller for raw streaming afterwards (e.g. the "exec" op). The
// returned reader must be used for reading the stream: it may already hold
// bytes that arrived immediately after the response. The connection has no
// read/write deadline so the caller can block indefinitely.
func Dial(path string, req Request) (net.Conn, *bufio.Reader, Response, error) {
	conn, err := DialSocket(path)
	if err != nil {
		return nil, nil, Response{}, err
	}

	if err := conn.SetDeadline(time.Now().Add(DefaultTimeout)); err != nil {
		conn.Close()
		return nil, nil, Response{}, err
	}

	if err := WriteRequest(conn, req); err != nil {
		conn.Close()
		return nil, nil, Response{}, err
	}

	br := bufio.NewReader(conn)
	resp, err := ReadResponse(br)
	if err != nil {
		conn.Close()
		return nil, nil, Response{}, err
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, nil, Response{}, err
	}
	return conn, br, resp, nil
}

func Ping(path string) error {
	resp, err := Call(path, Request{Op: "ping"})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// ManagerVersion returns the wire protocol version of the pair manager at
// path. Managers older than the versioning handshake report 0.
func ManagerVersion(path string) (int, error) {
	resp, err := Call(path, Request{Op: "status"})
	if err != nil {
		return 0, err
	}
	if !resp.OK {
		return 0, fmt.Errorf("%s", resp.Error)
	}
	return resp.Version, nil
}

// CheckVersion verifies that the manager of pair name speaks this
// client's wire protocol. Package upgrades do not restart managers of
// already-running pairs, so after an upgrade older pairs must be recreated.
func CheckVersion(name, path string) error {
	v, err := ManagerVersion(path)
	if err != nil {
		return fmt.Errorf("pair %q: %w", name, err)
	}
	if v != Version {
		return fmt.Errorf("pair %q is managed by an older netpair-manager (protocol %d, this netpair speaks %d);\na running pair keeps its manager until it is recreated:\n  netpair destroy %s && netpair create %s",
			name, v, Version, name, name)
	}
	return nil
}

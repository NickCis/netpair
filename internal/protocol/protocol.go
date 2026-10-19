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

type Request struct {
	Op      string   `json:"op"`
	Argv    []string `json:"argv,omitempty"`
	ID      string   `json:"id,omitempty"`
	Target  string   `json:"target,omitempty"`
	Session string   `json:"session,omitempty"`
	Cmd     string   `json:"cmd,omitempty"`
	Args    []string `json:"args,omitempty"`
}

type ProcessInfo struct {
	ID        string `json:"id"`
	PID       int    `json:"pid"`
	Argv      []string `json:"argv"`
	Running   bool   `json:"running"`
	ExitCode  int    `json:"exit_code,omitempty"`
}

type Response struct {
	OK       bool          `json:"ok"`
	Error    string        `json:"error,omitempty"`
	ID       string        `json:"id,omitempty"`
	PID      int           `json:"pid,omitempty"`
	Output   string        `json:"output,omitempty"`
	ExitCode int           `json:"exit_code,omitempty"`
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

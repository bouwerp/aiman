package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/google/uuid"
)

func Call(socketPath, method string, params any) (Response, error) {
	if _, err := os.Stat(socketPath); err != nil {
		return Response{}, fmt.Errorf("%w: %s", ErrServerNotRunning, socketPath)
	}
	rawParams := []byte("{}")
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return Response{}, err
		}
		rawParams = b
	}
	req := Request{ID: uuid.NewString(), Method: method, Params: rawParams}
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrServerNotRunning, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := conn.Write(append(body, '\n')); err != nil {
		return Response{}, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

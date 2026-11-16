package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/bouwerp/aiman/internal/server"
)

// rpcStreamHello is the first line `aiman rpc --stream` writes. The laptop
// reads a remote that exits before printing it as a binary without --stream.
const rpcStreamHello = `{"stream":1}`

// rpcStreamMaxInFlight bounds the calls one stream runs at once; a call past
// it is answered server_busy without dialling serve.
const rpcStreamMaxInFlight = 8

// rpcStreamMaxLine bounds one request line.
const rpcStreamMaxLine = 8 << 20

type rpcStreamRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcStreamAnswer struct {
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result,omitempty"`
	Error  *server.Error   `json:"error,omitempty"`
}

// runRPCStream implements `aiman rpc --stream`: one request line in, one answer
// line out per call, answered in completion order. Each call dials serve on its
// own, so serve's protocol and admission see ordinary calls.
func runRPCStream(stdin io.Reader, stdout io.Writer) error {
	out := bufio.NewWriter(stdout)
	var outMu sync.Mutex
	write := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		outMu.Lock()
		defer outMu.Unlock()
		_, _ = out.Write(append(b, '\n'))
		_ = out.Flush()
	}

	outMu.Lock()
	_, err := out.WriteString(rpcStreamHello + "\n")
	if err == nil {
		err = out.Flush()
	}
	outMu.Unlock()
	if err != nil {
		return err
	}

	sock, sockErr := socketPath()
	slots := make(chan struct{}, rpcStreamMaxInFlight)
	var wg sync.WaitGroup
	defer wg.Wait()

	in := bufio.NewReader(stdin)
	for {
		line, rerr := readStreamLine(in)
		if len(line) > 0 {
			handleStreamLine(line, sock, sockErr, slots, &wg, write)
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return rerr
		}
	}
}

// readStreamLine reads one newline-terminated line, refusing lines over
// rpcStreamMaxLine by draining them and returning an empty line.
func readStreamLine(in *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := in.ReadSlice('\n')
		if len(buf)+len(chunk) <= rpcStreamMaxLine {
			buf = append(buf, chunk...)
		} else {
			buf = buf[:0:0]
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return buf, err
	}
}

func handleStreamLine(line []byte, sock string, sockErr error, slots chan struct{}, wg *sync.WaitGroup, write func(any)) {
	var req rpcStreamRequest
	if err := json.Unmarshal(line, &req); err != nil || len(req.ID) == 0 {
		// No id to answer under; the laptop's call times out on its own.
		return
	}
	refuse := func(code, msg string) {
		write(rpcStreamAnswer{ID: req.ID, Error: &server.Error{Code: code, Message: msg}})
	}
	params, err := rpcParams(req.Method, req.Params)
	if err != nil {
		refuse(server.CodeInvalidParams, err.Error())
		return
	}
	if sockErr != nil {
		refuse(server.CodeServerNotRunning, sockErr.Error())
		return
	}
	select {
	case slots <- struct{}{}:
	default:
		refuse(server.CodeServerBusy, "rpc stream: too many calls in flight")
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() { <-slots }()
		resp, cerr := server.CallRaw(sock, req.Method, params)
		if cerr != nil {
			refuse(server.CodeServerNotRunning, cerr.Error())
			return
		}
		write(rpcStreamAnswer{ID: req.ID, Result: resp.Result, Error: resp.Error})
	}()
}

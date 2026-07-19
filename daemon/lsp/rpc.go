package lsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// JSON-RPC 2.0 framing over stdio: each message is preceded by
// "Content-Length: <n>\r\n\r\n" followed by exactly <n> bytes of JSON body.

// rpcMessage is the union of every JSON-RPC message shape we read from a server:
// a response (ID + Result/Error), a server->client request (ID + Method), or a
// notification (Method only). ID is kept raw so it can be either an integer (ours)
// or a string, and echoed back verbatim when we must reply.
type rpcMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// outgoing is a request (ID set) or notification (ID nil) we send to a server.
type outgoing struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      *int64      `json:"id,omitempty"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// outReply answers a server->client request. Result may be nil (encoded as null),
// which servers accept as an empty success.
type outReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result"`
}

// writeFrame emits a single Content-Length framed message. The caller is
// responsible for serializing concurrent writes to w.
func writeFrame(w io.Writer, body []byte) error {
	if _, err := io.WriteString(w, "Content-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

// readFrame parses one framed message: it reads headers line by line until the
// blank separator, then reads exactly Content-Length bytes of body.
func readFrame(r *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" { // blank line terminates the header block
			break
		}
		if colon := strings.IndexByte(line, ':'); colon >= 0 {
			key := strings.TrimSpace(line[:colon])
			if strings.EqualFold(key, "Content-Length") {
				n, err := strconv.Atoi(strings.TrimSpace(line[colon+1:]))
				if err != nil {
					return nil, fmt.Errorf("lsp: bad Content-Length: %w", err)
				}
				contentLength = n
			}
		}
	}
	if contentLength < 0 {
		return nil, errors.New("lsp: missing Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

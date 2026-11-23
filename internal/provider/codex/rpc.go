package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// conn is a JSON-RPC 2.0 connection to app-server, one message a line on
// stdio, without the "jsonrpc" member, as app-server speaks it. Calls go
// one way and server requests the other, each side with its own ids.
type conn struct {
	w   io.Writer
	wmu sync.Mutex

	mu    sync.Mutex
	next  int64
	calls map[int64]chan message
	done  chan struct{}
	err   error // why the connection ended; set before done closes

	// handle takes every notification and server request, in the order
	// app-server sent them, on the goroutine that reads. It must not
	// block on the connection.
	handle func(m message)
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// rpcError is an error answer, from either side.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (%d)", e.Message, e.Code) }

// errClosed is what a call gets once the connection has ended.
var errClosed = errors.New("codex: the connection to app-server ended")

func newConn(w io.Writer, handle func(message)) *conn {
	return &conn{w: w, calls: map[int64]chan message{}, done: make(chan struct{}), handle: handle}
}

// line takes one message app-server sent.
func (c *conn) line(b []byte) {
	var m message
	if json.Unmarshal(b, &m) == nil {
		c.dispatch(m)
	}
}

func (c *conn) dispatch(m message) {
	if m.Method != "" {
		c.handle(m)
		return
	}
	id, err := strconv.ParseInt(string(m.ID), 10, 64)
	if err != nil {
		return
	}
	c.mu.Lock()
	ch := c.calls[id]
	delete(c.calls, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- m
	}
}

func (c *conn) end(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		return
	default:
	}
	c.err = err
	close(c.done)
}

// Done is closed once the connection has ended.
func (c *conn) Done() <-chan struct{} { return c.done }

// call sends method with params and decodes its result into out, if out
// is not nil. It returns an *rpcError when app-server answers with one.
func (c *conn) call(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return errClosed
	default:
	}
	c.next++
	id := c.next
	ch := make(chan message, 1)
	c.calls[id] = ch
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.calls, id)
		c.mu.Unlock()
	}
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		forget()
		return err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(m.Result, out); err != nil {
			return fmt.Errorf("codex: %s answered %s: %w", method, m.Result, err)
		}
		return nil
	case <-c.done:
		forget()
		return errClosed
	case <-ctx.Done():
		forget()
		return ctx.Err()
	}
}

// notify sends a notification, which takes no answer.
func (c *conn) notify(method string, params any) error {
	m := map[string]any{"method": method}
	if params != nil {
		m["params"] = params
	}
	return c.send(m)
}

// reply answers the server request id with result.
func (c *conn) reply(id json.RawMessage, result any) error {
	return c.send(map[string]any{"id": id, "result": result})
}

// refuse answers the server request id with an error.
func (c *conn) refuse(id json.RawMessage, code int, text string) error {
	return c.send(map[string]any{"id": id, "error": rpcError{Code: code, Message: text}})
}

func (c *conn) send(m any) error {
	line, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.w.Write(append(line, '\n')); err != nil {
		c.end(errClosed)
		return errClosed
	}
	return nil
}

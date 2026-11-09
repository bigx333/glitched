package browsermcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"glitched/internal/jsonfile"
)

// The bridge is how the server reaches the app: a unix socket in the
// state directory both share, one call a connection, a line of JSON each
// way. Nothing authenticates a caller on it, so the directory's mode is
// its gate, as for the engine's socket (daemon.Start makes it 0700).

// Socket is the path of the app's browser socket in the state directory
// dir.
func Socket(dir string) string { return filepath.Join(dir, "browser.sock") }

// Command is the command line, of the program running, that serves the
// browser of the app on the state directory dir to an agent: both the
// glitched CLI and the app answer browser-mcp (Main). Nil where the
// program can't be found.
func Command(dir string) []string {
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return nil
	}
	return []string{self, "browser-mcp", "-dir", dir}
}

// Main is browser-mcp: it serves the browser of the app on the state
// directory -dir to the agent on in and out.
func Main(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("browser-mcp", flag.ContinueOnError)
	dir := fs.String("dir", "", "the app's state directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || fs.NArg() > 0 {
		return errors.New("usage: browser-mcp -dir DIR")
	}
	return Serve(ctx, in, out, Dial(*dir))
}

// WriteConfig writes the Claude Code MCP config (--mcp-config) that runs
// command as the browser's server, named ServerName, to a file in the
// state directory dir, and returns its path.
func WriteConfig(dir string, command []string) (string, error) {
	path := filepath.Join(dir, "browser-mcp.json")
	err := jsonfile.Write(path, map[string]any{"mcpServers": map[string]any{
		ServerName: map[string]any{"type": "stdio", "command": command[0], "args": command[1:]},
	}})
	return path, err
}

// ServerName is the browser's server among a run's MCP servers. It is not
// glitched mcp's "glitched", whose tools a run allows without asking: the
// browser's go through Claude Code's permissions, as
// mcp__glitched-browser__browser_*.
const ServerName = "glitched-browser"

// ErrNoApp is why a tool fails while the app isn't open.
var ErrNoApp = errors.New("The Glitched app isn't open on this machine, so its browser can't be reached. Ask the user to open Glitched, then try again.")

type bridgeRequest struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

type bridgeReply struct {
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// Dial is a Call through the socket of the app on the state directory dir.
func Dial(dir string) Call {
	return func(ctx context.Context, tool string, args json.RawMessage) (string, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "unix", Socket(dir))
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return "", ctx.Err()
		case denied(err):
			return "", fmt.Errorf("The Glitched app's browser can't be reached: %w", err)
		case err != nil:
			return "", ErrNoApp
		}
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
		defer stop()
		if err := json.NewEncoder(conn).Encode(bridgeRequest{tool, args}); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("browser: %w", err)
		}
		var r bridgeReply
		dec := json.NewDecoder(bufio.NewReaderSize(conn, 64<<10))
		if err := dec.Decode(&r); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", errors.New("The Glitched app closed before it answered.")
		}
		if r.Error != "" {
			return "", errors.New(r.Error)
		}
		return r.Text, nil
	}
}

// Listen listens on the browser socket of the state directory dir. A
// socket file an app left as it died is removed; one another app still
// answers on is an error.
func Listen(dir string) (net.Listener, error) {
	path := Socket(dir)
	if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
		conn.Close()
		return nil, fmt.Errorf("browser: another Glitched app serves its browser at %s", path)
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// Only this user reaches it, as the engine's socket. Windows has no
	// modes: the directory's ACL is the gate there.
	if runtime.GOOS == "windows" {
		return ln, nil
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Accept answers calls on ln with call until ln closes, each connection
// on its own. A call's ctx ends as the connection's caller hangs up.
func Accept(ln net.Listener, call Call) error {
	var delay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			// An error that passes, as too many files open, waits and
			// tries again, as net/http does: the socket stops answering
			// only as the app closes it, or calls would wait on it.
			if ne, ok := err.(interface{ Temporary() bool }); ok && ne.Temporary() {
				delay = min(max(2*delay, 5*time.Millisecond), time.Second)
				time.Sleep(delay)
				continue
			}
			ln.Close()
			return err
		}
		delay = 0
		go answer(conn, call)
	}
}

// requestTime is how long a caller has to send its request.
var requestTime atomic.Int64

func init() { requestTime.Store(int64(10 * time.Second)) }

func answer(conn net.Conn, call Call) {
	defer conn.Close()
	var req bridgeRequest
	rd := bufio.NewReaderSize(conn, 64<<10)
	conn.SetReadDeadline(time.Now().Add(time.Duration(requestTime.Load())))
	line, err := readLine(rd, maxMessage)
	if err != nil || json.Unmarshal(line, &req) != nil {
		return
	}
	conn.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The caller sends nothing more that matters: its connection
	// ending is it hanging up.
	go func() {
		io.Copy(io.Discard, rd)
		cancel()
	}()
	text, err := call(ctx, req.Tool, req.Args)
	r := bridgeReply{Text: text}
	if err != nil {
		r = bridgeReply{Error: err.Error()}
	}
	json.NewEncoder(conn).Encode(r)
}

// readLine reads a line of at most n bytes from r.
func readLine(r *bufio.Reader, n int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > n {
			return nil, errors.New("browser: request too long")
		}
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
}

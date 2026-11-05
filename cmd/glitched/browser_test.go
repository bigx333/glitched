package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/browsermcp"
)

// The engine gives each Claude Code run this program's browser-mcp, on
// its own state directory, which reaches the app on that directory.
func TestBrowserMCPReachesTheAppOnTheDir(t *testing.T) {
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Chdir(filepath.Dir(dir))
	cmd := browserCommand(filepath.Base(dir))
	self, _ := os.Executable()
	if want := []string{self, "browser-mcp", "-dir", dir}; !slices.Equal(cmd, want) {
		t.Errorf("the browser's command is %q, want %q", cmd, want)
	}
	if commands[cmd[1]] == nil {
		t.Fatalf("no %s command", cmd[1])
	}

	ln, err := browsermcp.Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go browsermcp.Accept(ln, func(_ context.Context, tool string, args json.RawMessage) (string, error) {
		return "the app's " + tool + " " + string(args), nil
	})

	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	t.Cleanup(func() { os.Stdin, os.Stdout = stdin, stdout })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- commands[cmd[1]](ctx, cmd[2:]); outW.Close() }()
	inW.WriteString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_snapshot","arguments":{"tab":1}}}` + "\n")
	line, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, `"text":"the app's browser_snapshot {\"tab\":1}"`) {
		t.Errorf("the call answered %s", line)
	}
	inW.Close()
	if err := <-done; err != nil {
		t.Errorf("browser-mcp: %v", err)
	}
}

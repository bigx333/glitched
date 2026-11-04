// Command guardhelper is a program a test builds and runs: it is not
// under go test itself, and opens a Claude Code run on the claude it is
// given, then runs that claude.
package main

import (
	"context"
	"fmt"
	"os"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

func main() {
	_, err := (&claude.Adapter{Path: os.Args[1]}).Open(context.Background(),
		provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Workspace: os.TempDir()})
	fmt.Println("open:", err)
	fmt.Println("command:", claude.Command(context.Background(), os.Args[1]).Run())
}

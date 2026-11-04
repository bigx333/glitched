// Command guardhelper is a program a test builds and runs: it is not
// under go test itself, and runs the claude it is given.
package main

import (
	"context"
	"os"

	"glitched/internal/provider/claude"
)

func main() {
	claude.Command(context.Background(), os.Args[1]).Run()
}

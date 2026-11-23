package daemon

import (
	"context"
	"os"
	"os/exec"
	"strconv"
)

// inhibit holds the Mac out of idle sleep until ctx ends, the display
// free to sleep: caffeinate -i, which also lets go once the engine's
// process is gone, should the engine die without ending ctx.
func inhibit(ctx context.Context) error {
	return exec.CommandContext(ctx, "/usr/bin/caffeinate", "-i", "-w", strconv.Itoa(os.Getpid())).Run()
}

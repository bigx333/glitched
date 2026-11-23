package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"glitched/internal/cliupdate"
	"glitched/internal/provider"
	"glitched/internal/runtime"
	"glitched/internal/transport"
)

// updateTimeout bounds one run of a CLI's updater.
var updateTimeout = 10 * time.Minute

// maxOutput is how much of an updater's output, its end, is kept.
const maxOutput = 64 << 10

// clis serves the engine's providers and the CLIs they run on this
// machine: what each said of itself, its versions, and its update.
type clis struct {
	ctx     context.Context // the engine's: an update runs until it ends, whoever waits for it
	mux     provider.Mux
	host    *runtime.Host
	checker *cliupdate.Checker

	mu       sync.Mutex
	updating map[string]chan struct{} // by provider, held while its update runs
}

func newCLIs(ctx context.Context, mux provider.Mux, host *runtime.Host, checker *cliupdate.Checker) *clis {
	return &clis{ctx: ctx, mux: mux, host: host, checker: checker, updating: map[string]chan struct{}{}}
}

// Providers asks every provider at once, each time: the app asks seldom,
// and keeps what it was told. A fresh ask checks for a newer version
// too, however recently it did.
func (c *clis) Providers(ctx context.Context, fresh, check bool) []transport.ProviderInfo {
	names := c.mux.Names()
	out := make([]transport.ProviderInfo, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() {
			if f, ok := c.mux[name].(provider.Forgetter); ok && fresh {
				f.Forget()
			}
			out[i] = c.describe(ctx, name, check, fresh)
		})
	}
	wg.Wait()
	return out
}

func (c *clis) describe(ctx context.Context, name string, check, force bool) transport.ProviderInfo {
	info, err := c.mux.Describe(ctx, name)
	p := transport.ProviderInfo{Name: name, Info: info}
	switch {
	case errors.Is(err, provider.ErrNotInstalled):
		p.State = transport.ProviderNotInstalled
	case errors.Is(err, provider.ErrSignedOut):
		p.State = transport.ProviderSignedOut
	}
	if err != nil {
		p.Error = err.Error()
	}
	if check && info.Path != "" {
		st := c.checker.Check(ctx, name, info.Version, installOf(name, info.Path), force)
		p.Update = &st
	}
	return p
}

// installOf is how name's CLI at path was installed on this machine.
func installOf(name, path string) cliupdate.Install {
	home, _ := os.UserHomeDir()
	return cliupdate.Detect(name, path, home, goruntime.GOOS, goruntime.GOARCH)
}

// UpdateProvider runs the updater of name's CLI, the way it was
// installed, then asks the provider again. It runs none while a turn of
// the provider runs on this host, and starts none until it's done. One
// update of a provider runs at a time: another waits for it, then runs
// its own, which finds the CLI current.
func (c *clis) UpdateProvider(ctx context.Context, name string, whenIdle bool) (transport.UpdateResult, error) {
	c.mu.Lock()
	running := c.updating[name]
	if running == nil {
		running = make(chan struct{}, 1)
		c.updating[name] = running
	}
	c.mu.Unlock()
	select {
	case running <- struct{}{}:
	case <-ctx.Done():
		return transport.UpdateResult{}, ctx.Err()
	}
	defer func() { <-running }()

	info, err := c.mux.Describe(ctx, name)
	switch {
	case errors.Is(err, provider.ErrNoAdapter):
		return transport.UpdateResult{}, err
	case err != nil && info.Path == "":
		return transport.UpdateResult{Error: err.Error(), Provider: c.describe(ctx, name, true, false)}, nil
	}
	in := installOf(name, info.Path)
	if in.Manual != "" {
		return transport.UpdateResult{Error: in.Manual, Provider: c.describe(ctx, name, true, false)}, nil
	}
	release, err := c.host.Hold(ctx, name, whenIdle)
	if errors.Is(err, runtime.ErrBusy) {
		return transport.UpdateResult{
			Busy:     true,
			Error:    fmt.Sprintf("%s has a turn running on this machine, so Glitched won't update it now. Update when idle waits for the turn to end.", cliupdate.Name(name)),
			Provider: c.describe(ctx, name, true, false),
		}, nil
	}
	if err != nil {
		return transport.UpdateResult{}, err
	}
	// The caller may have gone while the provider was asked.
	if err := ctx.Err(); err != nil {
		release()
		return transport.UpdateResult{}, err
	}
	out, err := run(c.ctx, in.Command)
	release()
	if f, ok := c.mux[name].(provider.Forgetter); ok {
		f.Forget()
	}
	res := transport.UpdateResult{Output: out, Provider: c.describe(ctx, name, true, false)}
	if err != nil {
		res.Error = err.Error()
	}
	return res, nil
}

// run runs command with no input, for updateTimeout at most, and returns
// the end of what it printed.
func run(ctx context.Context, command []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	var out tail
	cmd.Stdout, cmd.Stderr = &out, &out
	// A child the updater leaves holding its output would hold Wait.
	cmd.WaitDelay = 5 * time.Second
	// The updater's children, an installer it starts, end with it: none
	// writes to the CLI once its hold is released.
	end := ownGroup(cmd)
	err := cmd.Run()
	end()
	shown := strings.Join(command, " ")
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = fmt.Errorf("%s ran for %s and was stopped.", shown, updateTimeout)
	case err != nil:
		err = fmt.Errorf("%s failed: %v.", shown, err)
	}
	return string(out), err
}

// tail keeps the last maxOutput bytes written to it.
type tail []byte

func (t *tail) Write(p []byte) (int, error) {
	*t = append(*t, p...)
	if len(*t) > maxOutput {
		*t = append((*t)[:0], (*t)[len(*t)-maxOutput:]...)
	}
	return len(p), nil
}

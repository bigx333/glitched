package daemon

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"glitched/internal/provider"
	"glitched/internal/transport"
)

// versionTimeout bounds a program's answer to --version.
const versionTimeout = 15 * time.Second

// launches serves how each provider's program starts: engine.json's
// providers, checked, and handed to the adapters as they run.
type launches struct {
	dir      string
	mux      provider.Mux
	commands *commandLists // whose listings a new launch makes stale
	mu       sync.Mutex    // one save at a time, so the file and the adapters agree
}

func (s *launches) Launches(ctx context.Context) ([]transport.LaunchInfo, error) {
	cfg, err := Defaults(s.dir)
	if err != nil {
		return nil, err
	}
	var out []transport.LaunchInfo
	for _, name := range s.mux.Names() {
		if p, ok := s.mux[name].(provider.Placed); ok {
			out = append(out, check(ctx, name, p, cfg.Launch[name]))
		}
	}
	return out, nil
}

func (s *launches) SetLaunch(ctx context.Context, name string, l provider.Launch) (transport.LaunchInfo, error) {
	p, ok := s.mux[name].(provider.Placed)
	if !ok {
		return transport.LaunchInfo{}, fmt.Errorf("daemon: this engine starts no program for %q", name)
	}
	l = provider.Launch{Path: strings.TrimSpace(l.Path), Home: strings.TrimSpace(l.Home), Args: l.Args}
	if strings.TrimSpace(l.Args) == "" {
		l.Args = ""
	}
	info := check(ctx, name, p, l)
	if info.Errors != (provider.LaunchError{}) {
		return info, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := Edit(s.dir, func(cfg *Config) error {
		// A caller who gave up, as a client that timed out, is told
		// nothing was saved, so nothing is.
		if err := ctx.Err(); err != nil {
			return err
		}
		cfg.Launch = maps.Clone(cfg.Launch)
		if cfg.Launch == nil {
			cfg.Launch = map[string]provider.Launch{}
		}
		if l == (provider.Launch{}) {
			delete(cfg.Launch, name)
		} else {
			cfg.Launch[name] = l
		}
		return nil
	}); err != nil {
		return info, err
	}
	p.Place(l)
	s.commands.forget(name)
	info.Saved = true
	return info, nil
}

// check is l as p would start: what it runs, and what is wrong with it. A
// program l names must answer --version; one found on PATH that doesn't
// is the program's own trouble, which the provider's state shows.
func check(ctx context.Context, name string, p provider.Placed, l provider.Launch) transport.LaunchInfo {
	pl := p.Placement()
	info := transport.LaunchInfo{Provider: name, Launch: l, HomeVar: pl.HomeVar, DefaultHome: pl.DefaultHome, Reserved: pl.Flags()}
	prog, err := pl.Program(l)
	var bad *provider.LaunchError
	switch {
	case errors.As(err, &bad):
		info.Errors = *bad
		return info
	case err != nil:
		return info
	}
	info.Path, info.Env, info.Command = prog.Path, prog.Env, p.Command(prog)
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	v, err := p.Version(ctx, prog)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil && l.Path != "" {
		info.Errors.Path = fmt.Sprintf("%s didn't answer --version: %v", prog.Path, err)
	}
	info.Version = v
	return info
}

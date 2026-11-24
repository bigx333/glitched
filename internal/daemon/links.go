package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/jsonfile"
	"glitched/internal/transport"
)

// A Machine is another machine's engine this one holds a link to, where its
// agents may start children. Label is for people; Engine and Link are what
// that engine said it and the link are when the machine was added, and what
// a later connection must find.
type Machine struct {
	Label  string `json:"label"`
	Server string `json:"server"` // its tailnet address
	Engine api.ID `json:"engine"`
	Link   api.ID `json:"link"`
	Token  string `json:"token"`
}

// machinesFile keeps the Machines with their tokens, in one file so an add
// or a remove is one write, mode 0600 as tokens.json: whoever reads it
// reaches every thread a link made, as far as the link's scope allows.
const machinesFile = "machines.json"

// Machines are the machines added in dir, tokens included.
func Machines(dir string) ([]Machine, error) {
	var ms []Machine
	if err := jsonfile.Load(filepath.Join(dir, machinesFile), &ms); err != nil {
		return nil, err
	}
	return ms, nil
}

// places finds a remote child's machine in dir's machines.json as the
// file is at its create, so a machine removed takes no more: its engine
// and link, and never its token. A file that can't be read, or holds a
// machine without its label, server, engine or link, refuses the create,
// and is logged once until it reads again.
func places(dir string) engine.Places {
	var logged atomic.Bool
	return func(label string) (api.Remote, bool, error) {
		ms, err := Machines(dir)
		if i := slices.IndexFunc(ms, func(m Machine) bool { return m.Label == "" || m.Server == "" || m.Engine.IsZero() || m.Link.IsZero() }); err == nil && i >= 0 {
			err = fmt.Errorf("machine %d has no label, server, engine or link", i+1)
		}
		if err != nil {
			if !logged.Swap(true) {
				slog.Warn("daemon: machines.json can't be read; no child is placed on another machine", "err", err)
			}
			return api.Remote{}, false, fmt.Errorf("machines.json can't be read: %w", err)
		}
		logged.Store(false)
		i := slices.IndexFunc(ms, func(m Machine) bool { return m.Label == label })
		if i < 0 {
			return api.Remote{}, false, nil
		}
		return api.Remote{Engine: ms[i].Engine, Link: ms[i].Link}, true, nil
	}
}

// AddMachine adds the engine at server as label, with a token its person
// minted for this machine with pair -link. It asks that engine, through
// the link, which engine it is and which link the token is, and refuses a
// token that reaches the whole engine: an agent here may hold no more. It
// dials as a client does, so it reaches a machine through an installed
// Tailscale or on loopback, never through an engine's own node.
func AddMachine(ctx context.Context, dir, label, server, token string) (Machine, error) {
	if strings.TrimSpace(label) == "" {
		return Machine{}, errors.New("link: a machine needs a label")
	}
	ep := transport.NewEndpointClient(transport.Tailnet{Address: server, Token: token}, api.ID{})
	env, err := ep.Info(ctx)
	ep.Close()
	if err != nil {
		return Machine{}, fmt.Errorf("link: %s: %w", server, err)
	}
	if env.Link.IsZero() {
		return Machine{}, fmt.Errorf("link: the token reaches all of the engine at %s, not a link; mint a link there with glitched pair -name <this machine> -link", server)
	}
	m := Machine{Label: label, Server: server, Engine: env.ID, Link: env.Link, Token: token}
	_, err = jsonfile.Edit(filepath.Join(dir, machinesFile), func(ms *[]Machine) error {
		for _, o := range *ms {
			switch {
			case o.Label == label:
				return fmt.Errorf("link: %s is already added; remove it first", label)
			case o.Link == m.Link:
				return fmt.Errorf("link: that link is already added as %s", o.Label)
			}
		}
		*ms = append(*ms, m)
		return nil
	})
	if err != nil {
		return Machine{}, err
	}
	return m, nil
}

// RemoveMachine removes label's machine and its token. The link stays
// valid on that machine until its person revokes it there.
func RemoveMachine(dir, label string) error {
	_, err := jsonfile.Edit(filepath.Join(dir, machinesFile), func(ms *[]Machine) error {
		i := slices.IndexFunc(*ms, func(m Machine) bool { return m.Label == label })
		if i < 0 {
			return fmt.Errorf("link: no machine is labelled %s", label)
		}
		*ms = slices.Delete(*ms, i, i+1)
		return nil
	})
	return err
}

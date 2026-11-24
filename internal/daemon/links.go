package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"glitched/internal/api"
	"glitched/internal/jsonfile"
	"glitched/internal/transport"
)

// A Machine is another machine's engine this one holds a link to. Label is
// for people; Engine and Link are what that engine said it and the link
// are when the machine was added, and what a later connection must find.
type Machine struct {
	Label  string `json:"label"`
	Server string `json:"server"` // its tailnet address
	Engine api.ID `json:"engine"`
	Link   api.ID `json:"link"`
}

// linksFile keeps the token of each link in Config.Machines, by the link's
// id, mode 0600 as tokens.json: whoever reads it reaches every thread the
// link made, as far as the link's scope allows.
const linksFile = "links.json"

// AddMachine adds the engine at server as label, with a token its person
// minted for this machine with pair -link. It asks that engine, through
// the link, which engine it is and which link the token is, and refuses a
// token that reaches the whole engine: an agent here may hold no more.
func AddMachine(ctx context.Context, dir, label, server, token string) (Machine, error) {
	if strings.TrimSpace(label) == "" {
		return Machine{}, errors.New("link: a machine needs a label")
	}
	if cfg, err := Defaults(dir); err != nil {
		return Machine{}, err
	} else if slices.ContainsFunc(cfg.Machines, func(m Machine) bool { return m.Label == label }) {
		return Machine{}, fmt.Errorf("link: %s is already added; remove it first", label)
	}
	ep := transport.NewEndpointClient(transport.Tailnet{Address: server, Token: token}, api.ID{})
	env, err := ep.Info(ctx)
	ep.Close()
	if err != nil {
		return Machine{}, fmt.Errorf("link: %s: %w", server, err)
	}
	if env.Link == (api.ID{}) {
		return Machine{}, fmt.Errorf("link: the token reaches all of the engine at %s, not a link; mint a link there with glitched pair -name <this machine> -link", server)
	}
	m := Machine{Label: label, Server: server, Engine: env.ID, Link: env.Link}
	if _, err := jsonfile.Edit(filepath.Join(dir, linksFile), func(ts *map[string]string) error {
		if *ts == nil {
			*ts = map[string]string{}
		}
		(*ts)[m.Link.String()] = token
		return nil
	}); err != nil {
		return Machine{}, err
	}
	_, err = Edit(dir, func(cfg *Config) error {
		if slices.ContainsFunc(cfg.Machines, func(o Machine) bool { return o.Label == label }) {
			return fmt.Errorf("link: %s is already added; remove it first", label)
		}
		cfg.Machines = append(cfg.Machines, m)
		return nil
	})
	if err != nil {
		return Machine{}, errors.Join(err, forgetLink(dir, m.Link))
	}
	return m, nil
}

// RemoveMachine removes label's machine and forgets its token. The link
// stays valid on that machine until its person revokes it there.
func RemoveMachine(dir, label string) error {
	var gone Machine
	if _, err := Edit(dir, func(cfg *Config) error {
		i := slices.IndexFunc(cfg.Machines, func(m Machine) bool { return m.Label == label })
		if i < 0 {
			return fmt.Errorf("link: no machine is labelled %s", label)
		}
		gone = cfg.Machines[i]
		cfg.Machines = slices.Delete(cfg.Machines, i, i+1)
		return nil
	}); err != nil {
		return err
	}
	return forgetLink(dir, gone.Link)
}

func forgetLink(dir string, link api.ID) error {
	_, err := jsonfile.Edit(filepath.Join(dir, linksFile), func(ts *map[string]string) error {
		if _, ok := (*ts)[link.String()]; !ok {
			return ErrUnchanged
		}
		delete(*ts, link.String())
		return nil
	})
	if errors.Is(err, ErrUnchanged) {
		return nil
	}
	return err
}

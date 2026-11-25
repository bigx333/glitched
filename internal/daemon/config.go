package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"glitched/internal/jsonfile"
	"glitched/internal/provider"
)

// legacyPaths are the keys engine.json kept each provider's program in
// before Config.Launch, by provider.
var legacyPaths = map[string]string{"claude": "claude_path", "codex": "codex_path", "antigravity": "agy_path"}

// read sets cfg from engine.json's bytes b, over what cfg has: the Paths
// of older files' claude_path, codex_path and agy_path too, where
// providers names none.
func (cfg *Config) read(b []byte) error {
	if err := json.Unmarshal(b, cfg); err != nil {
		return err
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(b, &legacy); err != nil {
		return err
	}
	for name, key := range legacyPaths {
		var path string
		if v, ok := legacy[key]; !ok || json.Unmarshal(v, &path) != nil || path == "" {
			continue
		}
		if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~") {
			// Older engines ran a relative path as exec does, from where
			// the engine started or on PATH; Launch takes only whole ones.
			if found, err := exec.LookPath(path); err == nil {
				if abs, err := filepath.Abs(found); err == nil {
					path = abs
				}
			}
		}
		if l := cfg.Launch[name]; l.Path == "" {
			if cfg.Launch == nil {
				cfg.Launch = map[string]provider.Launch{}
			}
			l.Path = path
			cfg.Launch[name] = l
		}
	}
	return nil
}

// write sets raw, engine.json as it was, to cfg: each key Config keeps,
// the legacy paths among them, is cfg's or gone, and every other key is
// left as it was, within each provider's launch too.
func (cfg Config) write(raw *map[string]json.RawMessage) error {
	providers, err := cfg.providers((*raw)["providers"])
	if err != nil {
		return err
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	var ours map[string]json.RawMessage
	if err := json.Unmarshal(b, &ours); err != nil {
		return err
	}
	if *raw == nil {
		*raw = map[string]json.RawMessage{}
	}
	for _, k := range configKeys() {
		delete(*raw, k)
	}
	maps.Copy(*raw, ours)
	delete(*raw, "providers")
	if providers != nil {
		(*raw)["providers"] = providers
	}
	return nil
}

// providers is engine.json's providers, was, with each provider's path,
// home and args set to cfg.Launch's: what else the file keeps of a
// provider, and of providers this engine doesn't run, stays. nil is none.
func (cfg Config) providers(was json.RawMessage) (json.RawMessage, error) {
	all := map[string]map[string]json.RawMessage{}
	if len(was) > 0 {
		if err := json.Unmarshal(was, &all); err != nil {
			return nil, fmt.Errorf("daemon: engine.json's providers: %w", err)
		}
	}
	for name := range cfg.Launch {
		if _, ok := all[name]; !ok {
			all[name] = nil
		}
	}
	for name, p := range all {
		if p == nil {
			p = map[string]json.RawMessage{}
			all[name] = p
		}
		l := cfg.Launch[name]
		for k, v := range map[string]string{"path": l.Path, "home": l.Home, "args": l.Args} {
			if v == "" {
				delete(p, k)
				continue
			}
			b, _ := json.Marshal(v)
			p[k] = b
		}
		if len(p) == 0 {
			delete(all, name)
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	return json.Marshal(all)
}

// configKeys are the keys of engine.json that Config reads.
func configKeys() []string {
	keys := []string{}
	for f := range reflect.TypeFor[Config]().Fields() {
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	for _, k := range legacyPaths {
		keys = append(keys, k)
	}
	return keys
}

// ErrUnchanged is what an Edit's change returns to write nothing, and
// what Edit returns then.
var ErrUnchanged = errors.New("daemon: engine.json unchanged")

// Edit changes engine.json in dir for every process that edits it, as
// jsonfile.Edit does: change gets the Config the file holds now, read as
// Defaults reads it. It returns the Config written.
//
// An Edit that turns children from other machines on, or leaves them off
// (a retry, or a change to anything else, included), removes every link's
// token before it writes. PairLink mints under the same lock, so no link
// is minted between the removal and the write: off ends every link, and
// on starts with none, even when the writer stops halfway.
func Edit(dir string, change func(*Config) error) (Config, error) {
	var cfg Config
	_, err := jsonfile.Edit(filepath.Join(dir, configFile), func(raw *map[string]json.RawMessage) error {
		var err error
		if cfg, err = Defaults(dir); err != nil {
			return err
		}
		was := cfg.ChildrenFromOtherMachines != nil
		err = change(&cfg)
		if err != nil && !errors.Is(err, ErrUnchanged) {
			return err
		}
		if !was || cfg.ChildrenFromOtherMachines == nil {
			if err := endLinks(dir); err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
		return cfg.write(raw)
	})
	return cfg, err
}

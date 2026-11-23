package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"glitched/internal/provider"
)

// cacheState is what a claude process's runs know of its prompt cache and
// of the user's own idle compaction settings, which hold for every run of
// the process. Only the run on it reads and sets it.
type cacheState struct {
	idle    bool          // the CLI's settings leave idle compaction on
	min     int64         // the context idle compaction waits for
	ttl     time.Duration // the cache's lifetime, as the last request that wrote it said
	limited bool          // Claude Code said the account nears its usage limit
}

// compactable is when a turn that sent used tokens may compact before
// its cache, which expires then, does: only a one-hour cache, as Claude
// Code's own idle compaction, and only where the user's settings would
// let Claude Code do it. Zero otherwise.
func (c *cacheState) compactable(used int64, expires time.Time) time.Time {
	if c == nil || !c.idle || c.limited || c.ttl != time.Hour || used < c.min {
		return time.Time{}
	}
	return expires
}

// idleSettings reads, as Claude Code does, whether the user turned its
// idle compaction off and the context it waits for: the environment the
// program gets, then each settings file's env and keys, the later one
// winning. Its doc: code.claude.com/docs/en/settings-reference.md#idlecompaction.
func idleSettings(prog provider.Program, workspace string) cacheState {
	env := map[string]string{}
	for _, kv := range append(os.Environ(), prog.Env...) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	home, _ := os.UserHomeDir()
	dir, global := env["CLAUDE_CONFIG_DIR"], filepath.Join(home, ".claude.json")
	if dir != "" {
		global = filepath.Join(dir, ".claude.json")
	} else {
		dir = filepath.Join(home, ".claude")
	}
	idle, auto := true, true
	for _, path := range []string{
		global,
		filepath.Join(dir, "settings.json"),
		filepath.Join(workspace, ".claude", "settings.json"),
		filepath.Join(workspace, ".claude", "settings.local.json"),
	} {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var s struct {
			IdleCompaction     *bool             `json:"idleCompaction"`
			AutoCompactEnabled *bool             `json:"autoCompactEnabled"`
			Env                map[string]string `json:"env"`
		}
		if json.Unmarshal(b, &s) != nil {
			continue
		}
		if s.IdleCompaction != nil {
			idle = *s.IdleCompaction
		}
		if s.AutoCompactEnabled != nil {
			auto = *s.AutoCompactEnabled
		}
		for k, v := range s.Env {
			env[k] = v
		}
	}
	on := func(k string) bool {
		v, _ := strconv.ParseBool(env[k])
		return v || env[k] == "1"
	}
	c := cacheState{idle: idle && auto && !on("DISABLE_AUTO_COMPACT") && !on("DISABLE_COMPACT"), min: 200_000}
	if n, err := strconv.ParseInt(strings.TrimSpace(env["CLAUDE_CODE_IDLE_COMPACT_MIN_TOKENS"]), 10, 64); err == nil {
		c.min = max(100_000, n)
	}
	return c
}

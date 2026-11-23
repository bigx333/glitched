package cliupdate

import (
	"context"
	"log/slog"
	"maps"
	"sync"
	"time"

	"glitched/internal/jsonfile"
)

// checkEvery is how often one CLI's source is asked, a failure included,
// unless the user asks again.
const checkEvery = 24 * time.Hour

// A Checker answers what's known of a CLI's versions, asking its source
// at most once a day, and keeps the answers in a file so a restart
// doesn't ask again.
type Checker struct {
	lookup Lookup // nil makes no lookups
	file   string
	now    func() time.Time

	mu     sync.Mutex
	cache  map[string]entry   // as in file, and kept here when it can't be written
	asking map[string]*asking // by cache key
}

// asking is a lookup in flight, which every check of its key waits for.
type asking struct {
	done chan struct{} // closed once e is set
	e    entry
}

// entry is a source's last answer: Latest, kept through a failed lookup
// after it, and when it was last asked.
type entry struct {
	Latest string    `json:"latest,omitempty"`
	At     time.Time `json:"at"`
}

// NewChecker keeps its answers in file. A nil lookup makes none, and then
// no Latest is ever known.
func NewChecker(lookup Lookup, file string) *Checker {
	var cache map[string]entry
	if err := jsonfile.Load(file, &cache); err != nil {
		slog.Warn("cliupdate: reading the cache", "err", err)
	}
	c := &Checker{lookup: lookup, file: file, now: time.Now, cache: map[string]entry{}, asking: map[string]*asking{}}
	maps.Copy(c.cache, cache)
	return c
}

// Check is what's known of provider's CLI, installed as in at version
// installed. It asks in's source when force, or when it was last asked a
// day ago or never. A source that can't be reached is no news.
func (c *Checker) Check(ctx context.Context, provider, installed string, in Install, force bool) Status {
	st := Status{Installed: installed, Command: in.Command, Manual: in.Manual}
	if c.lookup == nil || in.Source.URL == "" {
		return st
	}
	e := c.latest(ctx, provider+" "+in.Source.URL, in.Source, force)
	st.Latest, st.Checked = e.Latest, e.At
	newer, ok := Newer(installed, e.Latest)
	st.Available = newer && ok
	return st
}

func (c *Checker) latest(ctx context.Context, key string, s Source, force bool) entry {
	c.mu.Lock()
	a := c.asking[key]
	if a == nil {
		e, known := c.cache[key]
		if known && !force && c.now().Sub(e.At) < checkEvery {
			c.mu.Unlock()
			return e
		}
		a = &asking{done: make(chan struct{}), e: e}
		c.asking[key] = a
		// Not the asker's ctx: one who gives up leaves the lookup to the
		// others waiting, and records no failure for the day.
		go c.ask(context.WithoutCancel(ctx), key, s, a)
	}
	c.mu.Unlock()
	select {
	case <-a.done:
		return a.e
	case <-ctx.Done():
		return entry{}
	}
}

func (c *Checker) ask(ctx context.Context, key string, s Source, a *asking) {
	latest, err := c.lookup.Latest(ctx, s)
	if err != nil {
		slog.Info("cliupdate: no answer from the source", "url", s.URL, "err", err)
		latest = a.e.Latest
	}
	e := entry{Latest: latest, At: c.now()}
	c.mu.Lock()
	c.cache[key] = e
	if err := jsonfile.Write(c.file, c.cache); err != nil {
		slog.Warn("cliupdate: writing the cache", "err", err)
	}
	a.e = e
	delete(c.asking, key)
	c.mu.Unlock()
	close(a.done)
}

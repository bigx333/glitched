package client

import (
	"context"
	"errors"
	"time"

	"glitched/internal/api"
	"glitched/internal/projects"
	"glitched/internal/transport"
)

// ErrTooOld is an engine older than the call asked of it, which it can't
// answer until Glitched on its machine is updated.
var ErrTooOld = errors.New("client: the engine is too old for this; update Glitched on its machine")

// foldersTimeout bounds a listing a little past the engine's own bound,
// so that the engine says why it gave up.
const foldersTimeout = 15 * time.Second

// Folders lists the folders in path on e's machine; done gets them, or
// why not, on the UI thread.
func (m *Manager) Folders(e *Env, path string, done func(api.FolderList, error)) {
	p, err := projectsOf(e)
	if err != nil {
		done(api.FolderList{}, err)
		return
	}
	ectx := e.ctx
	m.wg.Go(func() {
		ctx, cancel := context.WithTimeout(ectx, foldersTimeout)
		defer cancel()
		list, err := p.Folders(ctx, api.FolderQuery{Path: path})
		err = tooOld(err)
		m.post(ectx, func() { done(list, err) })
	})
}

// Repos gives page each page of the GitHub repositories gh on e's
// machine is signed in to reach and done how the listing ended, both on
// the UI thread. cancel stops the listing; done then gets
// context.Canceled.
func (m *Manager) Repos(e *Env, page func(api.RepoPage), done func(error)) (cancel func()) {
	p, err := projectsOf(e)
	if err != nil {
		done(err)
		return func() {}
	}
	ectx := e.ctx
	ctx, cancel := context.WithTimeout(ectx, projects.ReposTimeout+5*time.Second)
	m.wg.Go(func() {
		defer cancel()
		err := p.Repos(ctx, func(p api.RepoPage) {
			m.post(ectx, func() { page(p) })
		})
		err = tooOld(err)
		m.post(ectx, func() { done(err) })
	})
	return cancel
}

// Clone clones a repository onto e's machine. progress gets each line
// the clone prints and done how it ended, both on the UI thread. cancel
// stops the clone, which takes away the folder it made; done then gets
// context.Canceled.
func (m *Manager) Clone(e *Env, req api.CloneRequest, progress func(string), done func(api.Cloned, error)) (cancel func()) {
	p, err := projectsOf(e)
	if err != nil {
		done(api.Cloned{}, err)
		return func() {}
	}
	ectx := e.ctx
	// A minute past the clone's own bound, so the engine says it gave up.
	ctx, cancel := context.WithTimeout(ectx, projects.CloneTimeout+time.Minute)
	m.wg.Go(func() {
		defer cancel()
		cloned, err := p.Clone(ctx, req, func(line string) {
			m.post(ectx, func() { progress(line) })
		})
		err = tooOld(err)
		m.post(ectx, func() { done(cloned, err) })
	})
	return cancel
}

// skillsTimeout bounds a read of a thread's skills, which a folder on a
// hung network mount would otherwise hold.
const skillsTimeout = 10 * time.Second

// Skills is what Glitched gives the agent of thread on e's machine, by
// that machine's engine, once a sync of this machine's skills there if
// sync says so; done gets it, or why not, on the UI thread.
func (m *Manager) Skills(e *Env, thread api.ID, sync bool, done func(api.SkillList, error)) {
	s := e.session
	if s == nil {
		done(api.SkillList{}, errNoEngine)
		return
	}
	k, ok := s.ep.(transport.Skills)
	if !ok {
		done(api.SkillList{}, ErrTooOld)
		return
	}
	ectx := e.ctx
	var synced <-chan struct{}
	if sync {
		synced = m.syncTo(e, s)
	}
	m.wg.Go(func() {
		if synced != nil {
			select {
			case <-synced:
			case <-time.After(syncFirst):
			case <-ectx.Done():
				return
			}
		}
		ctx, cancel := context.WithTimeout(ectx, skillsTimeout)
		defer cancel()
		list, err := k.Skills(ctx, api.SkillQuery{Thread: thread})
		err = tooOld(err)
		m.post(ectx, func() { done(list, err) })
	})
}

// projectsOf is the engine e is connected to, as Projects.
func projectsOf(e *Env) (transport.Projects, error) {
	s := e.session
	if s == nil {
		return nil, errNoEngine
	}
	p, ok := s.ep.(transport.Projects)
	if !ok {
		return nil, ErrTooOld
	}
	return p, nil
}

// tooOld is err as the user reads it: ErrTooOld for an engine that
// doesn't know the call, and the engine's own words for its failure.
func tooOld(err error) error {
	if errors.Is(err, transport.ErrUnknownMethod) {
		return ErrTooOld
	}
	if re := (*transport.RemoteError)(nil); errors.As(err, &re) {
		return errors.New(re.Message)
	}
	return err
}

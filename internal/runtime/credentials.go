package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/secrets"
)

// askPrefix marks the requests a host opens for glitched mcp's
// request_credentials, which it answers itself: their agent never sees
// the request, only the tool's result.
const askPrefix = "credentials:"

// askTimeout is how long a credentials request waits for the user.
var askTimeout = 30 * time.Minute

var errNoVault = errors.New("this engine keeps no credentials")

var errNotHere = errors.New("this turn does not run on this engine, or the same call is asking already")

type askKey struct {
	run   api.ID
	dedup string
}

// WithVault keeps the credentials users give the host's agents in v.
func WithVault(v *secrets.Vault) Option { return func(h *Host) { h.vault = v } }

// AskCredentials opens a credentials request in a's thread for the turn it
// runs at a.Access, and waits until the user answers it, the turn ends,
// ctx ends or askTimeout passes. A repeated call finds the same request,
// and the answer it got.
func (h *Host) AskCredentials(ctx context.Context, a api.Ask) (api.Asked, error) {
	if h.vault == nil {
		return api.Asked{}, errNoVault
	}
	if err := a.Credentials.Check(); err != nil {
		return api.Asked{}, err
	}
	if a.Call == "" {
		return api.Asked{}, errors.New("credentials: no call")
	}
	t, err := h.store.Thread(ctx, a.Thread)
	if err != nil {
		return api.Asked{}, err
	}
	live := t.Live()
	if live == nil {
		return api.Asked{}, errors.New("this thread has no turn running, so nothing can ask for it")
	}
	if live.Access != a.Access {
		return api.Asked{}, errors.New("this agent was started for an earlier turn of the thread, so it can't ask for the current one")
	}
	if a.Credentials.EnvFile != "" {
		if _, err := secrets.EnvFile(t.Workspace, a.Credentials.EnvFile); err != nil {
			return api.Asked{}, err
		}
	}
	key := askKey{live.ID, askPrefix + a.Call}
	for _, r := range t.Requests {
		if r.Run == key.run && r.Dedup == key.dedup && (r.State == api.RequestAnswered || r.State == api.RequestDelivered) {
			return h.asked(a.Thread, r.Answer)
		}
	}
	h.mu.Lock()
	lr := h.runs[key.run]
	h.mu.Unlock()
	if lr == nil {
		return api.Asked{}, errNotHere
	}
	// A quick agent asks while its start is still being recorded.
	select {
	case <-lr.started:
	case <-lr.ctx.Done():
		return api.Asked{}, errNotHere
	case <-ctx.Done():
		return api.Asked{}, ctx.Err()
	}
	h.mu.Lock()
	if lr.agent == nil || h.asks[key] != nil {
		h.mu.Unlock()
		return api.Asked{}, errNotHere
	}
	answered := make(chan json.RawMessage, 1)
	h.asks[key] = answered
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.asks, key)
		h.mu.Unlock()
	}()
	body, _ := json.Marshal(a.Credentials)
	body = secrets.RedactJSON(body, h.vault.Values(a.Thread))
	if err := h.observe(ctx, a.Thread, engine.Surfaced{Run: key.run, Request: api.NewID(), Dedup: key.dedup, Kind: api.KindCredentials, Body: body}, 0); err != nil {
		return api.Asked{}, err
	}
	withdraw := func(reason string) {
		h.record(a.Thread, engine.Withdrawn{Run: key.run, Dedup: key.dedup, Reason: reason}, 0)
	}
	timeout := time.NewTimer(askTimeout)
	defer timeout.Stop()
	select {
	case answer := <-answered:
		return h.asked(a.Thread, answer)
	case <-lr.steering.Done():
		return api.Asked{Status: api.AskCanceled}, nil
	case <-timeout.C:
		withdraw("No answer came in time.")
		return api.Asked{Status: api.AskTimedOut}, nil
	case <-ctx.Done():
		withdraw("The agent stopped waiting.")
		return api.Asked{}, ctx.Err()
	}
}

// asked is what the agent learns of answer: the names provided and where
// they are, never the values; only names the vault holds count.
func (h *Host) asked(thread api.ID, answer json.RawMessage) (api.Asked, error) {
	p, err := api.ParseProvided(answer)
	if err != nil {
		return api.Asked{}, err
	}
	if len(p.Names) == 0 {
		return api.Asked{Status: api.AskDeclined}, nil
	}
	held := h.vault.Values(thread)
	dir := h.vault.Dir(thread)
	out := api.Asked{Status: api.AskProvided, Env: filepath.Join(dir, secrets.EnvScript), Files: map[string]string{}}
	for _, name := range p.Names {
		if _, ok := held[name]; !ok {
			return api.Asked{}, errors.New("the answer names " + name + ", which the user did not provide")
		}
		out.Names = append(out.Names, name)
		out.Files[name] = filepath.Join(dir, name)
	}
	return out, nil
}

// ProvideCredentials keeps the values the user gave for a pending
// credentials request in the thread's vault, and answers the request with
// their names. The values go nowhere else.
func (h *Host) ProvideCredentials(ctx context.Context, p api.Provide) error {
	if h.vault == nil {
		return errNoVault
	}
	t, err := h.store.Thread(ctx, p.Thread)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(t.Requests, func(r engine.Request) bool { return r.ID == p.Request })
	if i < 0 {
		return api.ErrNotFound
	}
	r := t.Requests[i]
	switch {
	case r.Kind != api.KindCredentials:
		return errors.New("credentials: the request asks for no credentials")
	case r.Generation != p.Generation:
		return errors.New("credentials: the request belongs to another run")
	case r.State != api.RequestPending:
		return api.ErrAlreadyAnswered
	}
	c, err := api.ParseCredentials(r.Body)
	if err != nil {
		return err
	}
	if err := p.Check(c); err != nil {
		return err
	}
	h.mu.Lock()
	waiting := h.asks[askKey{r.Run, r.Dedup}] != nil
	h.mu.Unlock()
	if !waiting {
		h.record(p.Thread, engine.Withdrawn{Run: r.Run, Dedup: r.Dedup, Reason: "The agent stopped waiting."}, 0)
		return errors.New("the agent stopped waiting for these credentials; it can ask again")
	}
	if err := h.vault.Put(p.Thread, p.Values); err != nil {
		return err
	}
	if c.EnvFile != "" {
		path, err := secrets.EnvFile(t.Workspace, c.EnvFile)
		if err == nil {
			err = secrets.WriteDotenv(path, p.Values)
		}
		if err != nil {
			return err
		}
	}
	answer, _ := json.Marshal(api.Provided{Names: slices.Sorted(maps.Keys(p.Values))})
	sum := sha256.Sum256([]byte(p.Request.String() + "/provide"))
	_, err = h.Submit(ctx, api.Command{ID: api.ID(sum[:16]), Target: p.Thread, Body: api.RequestRespond{Request: r.ID, Generation: r.Generation, Answer: answer}})
	return err
}

// ForgetCredentials deletes every value thread's vault holds.
func (h *Host) ForgetCredentials(_ context.Context, thread api.ID) error {
	if h.vault == nil {
		return errNoVault
	}
	return h.vault.Forget(thread)
}

// answerAsk hands an answer to the AskCredentials that waits for it.
func (h *Host) answerAsk(b engine.DeliverAnswer) engine.Observation {
	h.mu.Lock()
	answered := h.asks[askKey{b.Run, b.Dedup}]
	h.mu.Unlock()
	if answered == nil {
		return engine.Unheard{Request: b.Request, Reason: "The agent stopped waiting before the answer reached it."}
	}
	select {
	case answered <- b.Answer:
	default:
	}
	return engine.Delivered{Request: b.Request}
}

// credentials are thread's values, for redaction; none without a vault.
func (h *Host) credentials(thread api.ID) map[string]string {
	if h.vault == nil {
		return nil
	}
	return h.vault.Values(thread)
}

func isAsk(dedup string) bool { return strings.HasPrefix(dedup, askPrefix) }

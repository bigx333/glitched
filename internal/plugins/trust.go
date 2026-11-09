package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	plugin "github.com/yxxbc/mygo-plugin"

	"glitched/internal/plugins/pack"
)

// Trust says why a plugin may run, or why it waits.
type Trust string

const (
	// TrustOwn: signed by this device's key, or one the user trusts.
	TrustOwn Trust = "own"
	// TrustConsented: signed by another key, and allowed for what it asks.
	TrustConsented Trust = "consented"
	// TrustWaiting: signed by another key, waiting for the user to allow
	// what it asks, as after an update that asks for more.
	TrustWaiting Trust = "waiting"
	// TrustUnsigned: no signature; the user may trust it as theirs.
	TrustUnsigned Trust = "unsigned"
	// TrustTampered: changed since another key signed it.
	TrustTampered Trust = "tampered"
)

// consent is what the user allowed a plugin of another key: the key, and
// everything it asked for then.
type consent struct {
	Key  string   `json:"key"`
	Asks []string `json:"asks"`
}

var (
	errUnsigned = errors.New("it isn't signed: trust it as yours in Settings › Plugins if you wrote it or know its author")
	errWaiting  = errors.New("it's someone else's: allow what it asks for in Settings › Plugins")
)

// asks is everything a plugin asks for, as consent records it: a new
// entry in an update asks the user again.
func asks(m plugin.Manifest, x Extension) []string {
	var out []string
	for _, c := range m.Capabilities {
		out = append(out, "capability:"+c)
	}
	for _, h := range m.Permissions.Network {
		out = append(out, "network:"+h)
	}
	for _, p := range x.Programs {
		out = append(out, "program:"+p)
	}
	for _, t := range x.Tools {
		out = append(out, "tool:"+t.Name)
	}
	if x.Service != "" {
		out = append(out, "service")
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// verify checks the folder's signature and says whether it may run, and
// the read-only snapshot to load it from. Your own plugins are re-signed
// as you change them, when the host signs (the window's does, the
// engine's leaves it to the window).
func (h *Host) verify(in *installed) (code string, err error) {
	// A plugin's folder may be a link to where it's written, as while
	// developing; a link inside it is still refused.
	folder, err := filepath.EvalSymlinks(in.folder)
	if err != nil {
		return "", err
	}
	st := pack.Check(folder)
	keys, kerr := pack.LoadKeys(h.state)
	if kerr != nil {
		return "", kerr
	}
	own := keys.Own.ID()
	if st.State == pack.Tampered && st.KeyID == own && h.opts.SignOwn {
		if err := pack.Sign(folder, keys.Own); err != nil {
			return "", err
		}
		st = pack.Check(folder)
	}
	switch st.State {
	case pack.Unsigned:
		in.trust = TrustUnsigned
		return "", errUnsigned
	case pack.Invalid:
		in.trust = TrustTampered
		return "", fmt.Errorf("its signature is broken: %w", st.Err)
	case pack.Tampered:
		in.trust = TrustTampered
		if st.KeyID == own {
			return "", fmt.Errorf("%s changed since it was signed; Glitched signs it again as it reloads", st.File)
		}
		return "", fmt.Errorf("it changed since its author signed it (%s): reinstall it", st.File)
	}
	in.signer = st.KeyID
	trusted := st.KeyID == own || slices.ContainsFunc(keys.Trusted, func(pub string) bool { return pack.KeyID(pub) == st.KeyID })
	switch {
	case trusted:
		in.trust = TrustOwn
	default:
		c, ok := h.prefs.Consent[in.manifest.ID]
		want := asks(in.manifest, in.ext)
		if !ok || c.Key != st.KeyID || !subset(want, c.Asks) {
			in.trust = TrustWaiting
			in.asks, in.waitingKey = want, st.KeyID
			return "", errWaiting
		}
		in.trust = TrustConsented
	}
	return pack.Snapshot(folder, pack.SnapshotsDir(h.state))
}

func subset(a, of []string) bool {
	for _, x := range a {
		if !slices.Contains(of, x) {
			return false
		}
	}
	return true
}

// TrustAsMine signs the plugin in folder with this device's key, as one
// the user wrote or vouches for, and reloads.
func (h *Host) TrustAsMine(folder string) error {
	if !slices.ContainsFunc(h.list(), func(in *installed) bool { return in.folder == folder }) {
		return fmt.Errorf("no plugin in %s", folder)
	}
	key, err := pack.LoadKey(h.state)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return err
	}
	if err := pack.Sign(real, key); err != nil {
		return err
	}
	h.Reload()
	return nil
}

// Allow records the user's consent to what the plugin id, signed by
// another key, asks for now, and reloads.
func (h *Host) Allow(id string) error {
	var c *consent
	for _, in := range h.list() {
		if in.manifest.ID == id && in.trust == TrustWaiting {
			c = &consent{Key: in.waitingKey, Asks: in.asks}
		}
	}
	if c == nil {
		return fmt.Errorf("plugin %s waits for no consent", id)
	}
	return h.edit(func(p *prefs) {
		if p.Consent == nil {
			p.Consent = map[string]consent{}
		}
		p.Consent[id] = *c
	})
}

// describeAsk is an entry of asks in words, for the consent sheet.
func describeAsk(a string) string {
	kind, what, _ := strings.Cut(a, ":")
	switch kind {
	case "capability":
		if c, ok := capability(what); ok {
			return c.About
		}
		if what == Network {
			return "Reach the network"
		}
	case "network":
		return "Reach " + what
	case "program":
		return "Run " + what + " as you"
	case "tool":
		return "Offer the tool " + what + " to your agents"
	case "service":
		return "Run in the background"
	}
	return a
}

// installer installs into the plugins folder, trusting this device's key
// and the keys the user trusts.
func (h *Host) installer() (*pack.Installer, error) {
	keys, err := pack.LoadKeys(h.state)
	if err != nil {
		return nil, err
	}
	return &pack.Installer{Dir: Dir(h.state), Keys: keys}, nil
}

// Install installs the plugin src names: a .glitched-plugin file, a link
// to one, a GitHub release or repository, or a git repository. It's
// verified before anything is written, and reloads. Another key's plugin
// waits, off, for the user to allow what it asks for. Install runs git
// and the network, so it belongs off the UI thread; it returns what the
// host does on it, to run there.
func (h *Host) Install(ctx context.Context, src pack.Source) (*pack.Result, error) {
	i, err := h.installer()
	if err != nil {
		return nil, err
	}
	return i.Install(ctx, src)
}

// CheckUpdates says which installed plugins have a later version where
// they came from, and what each newly asks for. Off the UI thread.
func (h *Host) CheckUpdates(ctx context.Context) ([]pack.Found, error) {
	i, err := h.installer()
	if err != nil {
		return nil, err
	}
	return i.CheckUpdates(ctx)
}

// Update installs the plugin id's later version, which must be signed by
// the same key. One that asks for more waits for the user again. Off the
// UI thread; Reload after, on it.
func (h *Host) Update(ctx context.Context, id string) (*pack.Result, error) {
	i, err := h.installer()
	if err != nil {
		return nil, err
	}
	return i.Update(ctx, id)
}

// Remove takes the plugin id away: its folder, its data and what it was
// allowed, and reloads.
func (h *Host) Remove(id string) error {
	var folder string
	for _, in := range h.list() {
		if in.manifest.ID == id {
			folder = in.folder
		}
	}
	if folder == "" {
		return fmt.Errorf("no plugin %q is installed", id)
	}
	h.closeAll()
	if err := os.RemoveAll(folder); err != nil {
		return err
	}
	os.RemoveAll(filepath.Join(DataDir(h.state), id))
	return h.edit(func(p *prefs) {
		delete(p.Grants, id)
		delete(p.Consent, id)
		delete(p.Settings, id)
		p.Disabled = slices.DeleteFunc(p.Disabled, func(s string) bool { return s == id })
	})
}

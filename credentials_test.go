package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

const probeValue = "probe-value-0123"

var stripeKey = api.Credentials{
	Reason: "The billing tests call Stripe's test API.",
	Fields: []api.Field{{Name: "STRIPE_KEY", Label: "Stripe test key", URL: "https://dashboard.stripe.com/test/apikeys"}},
}

// credentialing is the app on an engine whose agent asks for c through
// glitched mcp when sent a message, with the card waiting; and the
// engine's state dir.
func credentialing(t *testing.T, c api.Credentials) (*harness, thread, string) {
	t.Helper()
	return credentialingAt(t, c, "")
}

// credentialingAt is credentialing with the thread started at access, or
// at the default with none.
func credentialingAt(t *testing.T, c api.Credentials, access string) (*harness, thread, string) {
	t.Helper()
	dir := tempDir(t)
	fk := fake.New(nil)
	d := engine(t, daemon.Config{Dir: dir}, fk)
	fk.Asks(d.Endpoint().(fake.Asker), "Set up billing.", c)
	h := start(t, dir)
	if access != "" {
		h.a.access = access
	}
	h.a.draft = "Set up billing."
	h.tt.Frame()
	h.click("Send")
	h.until("the credentials card", func() bool {
		r := lastRequest(h.a.current())
		return h.tt.HasText("asks for credentials") && r != nil && r.State == api.RequestPending
	})
	h.tt.Advance(time.Second)
	return h, h.a.current(), dir
}

// typeInto focuses the card's field labeled label, the first, and types
// s into it.
func typeInto(t *testing.T, h *harness, label, s string) {
	t.Helper()
	f := h.a.credentials[lastRequest(h.a.current()).Request]
	if f == nil {
		t.Fatal("no form on the card")
	}
	f.fields[0].Focus()
	h.tt.Frame()
	if active, _ := h.tt.TextInputActive(); !active || !h.tt.Focused(label) {
		t.Fatalf("%q is not focused for typing", label)
	}
	h.tt.Type(s)
	h.tt.Frame()
}

// finished waits for the agent's turn to end after the card's answer.
func finished(t *testing.T, h *harness, th thread) {
	t.Helper()
	h.until("the turn to end", func() bool {
		r := lastRequest(th)
		return r.State == api.RequestDelivered && !h.a.current().View.Working()
	})
	h.tt.Advance(time.Second)
}

// noValue fails if the value shows anywhere the app draws or keeps.
func noValue(t *testing.T, h *harness, th thread, dir string) {
	t.Helper()
	for _, s := range h.tt.Texts() {
		if strings.Contains(s, probeValue) {
			t.Errorf("the window shows the value: %q", s)
		}
	}
	if strings.Contains(texts(entriesOf(th)), probeValue) {
		t.Errorf("the transcript holds the value: %q", texts(entriesOf(th)))
	}
	filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || strings.Contains(path, string(filepath.Separator)+"secrets"+string(filepath.Separator)) {
			return nil
		}
		if b, _ := os.ReadFile(path); strings.Contains(string(b), probeValue) {
			t.Errorf("%s holds the value", path)
		}
		return nil
	})
}

// The card shows the agent's reason, each field's label, name and link,
// a Provide that waits for every value, and Decline; and the value typed
// shows masked until Show.
func TestCredentialsCardMasksWhatIsTyped(t *testing.T) {
	h, th, dir := credentialing(t, stripeKey)
	for _, s := range []string{"Claude asks for credentials", stripeKey.Reason, "Stripe test key", "STRIPE_KEY", "Get it", "Provide", "Decline", "⇧⌘D"} {
		if !h.tt.HasText(s) {
			t.Errorf("no %q on the waiting card; texts %q", s, h.tt.Texts())
		}
	}
	h.click("Provide")
	h.tt.Advance(time.Second)
	if r := lastRequest(th); r.State != api.RequestPending {
		t.Fatalf("an empty Provide answered the card: %v", r.State)
	}
	typeInto(t, h, "Stripe test key", probeValue)
	noValue(t, h, th, dir)
	field := func() ui.AccessInfo {
		t.Helper()
		f, ok := h.tt.AccessByRole(ui.RoleTextField, "Stripe test key")
		if !ok {
			t.Fatal("no Stripe test key field")
		}
		return f
	}
	if f := field(); !f.Password || f.Value != "" {
		t.Errorf("the field reads %q, password %v; want it masked", f.Value, f.Password)
	}
	h.click("Show STRIPE_KEY")
	h.tt.Frame()
	if f := field(); f.Password || f.Value != probeValue {
		t.Errorf("after Show the field reads %q, password %v", f.Value, f.Password)
	}
	h.click("Hide STRIPE_KEY")
	h.tt.Frame()
	if f := field(); !f.Password {
		t.Error("Hide leaves the value shown")
	}
}

// Provide keeps the value in the thread's vault, where the agent's
// command reads it; what the agent echoes of it shows redacted, and the
// card names what it was given, never the value.
func TestCredentialsCardProvides(t *testing.T) {
	for _, tc := range []struct {
		name string
		do   func(h *harness)
	}{
		{"button", func(h *harness) { h.click("Provide") }},
		{"⌘⏎", func(h *harness) { h.tt.Key(ui.Cmd, ui.KeyEnter) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, th, dir := credentialing(t, stripeKey)
			typeInto(t, h, "Stripe test key", probeValue)
			tc.do(h)
			finished(t, h, th)
			if got := texts(entriesOf(th)); !strings.Contains(got, "STRIPE_KEY is [STRIPE_KEY].") {
				t.Errorf("the agent did not read its value, redacted: %q", got)
			}
			for _, s := range []string{"Provided", "STRIPE_KEY", "Forget"} {
				if !h.tt.HasText(s) {
					t.Errorf("no %q on the provided card; texts %q", s, h.tt.Texts())
				}
			}
			if len(h.a.credentials) != 0 {
				t.Errorf("the app keeps %d forms after Provide", len(h.a.credentials))
			}
			noValue(t, h, th, dir)
		})
	}
}

// Decline, by button, ⇧⌘D or Escape outside the fields, answers with no
// values, and the agent hears it declined.
func TestCredentialsCardDeclines(t *testing.T) {
	for _, tc := range []struct {
		name string
		do   func(h *harness)
	}{
		{"button", func(h *harness) { h.click("Decline") }},
		{"⇧⌘D", func(h *harness) { h.tt.Key(ui.Cmd|ui.Shift, ui.KeyD) }},
		{"Escape", func(h *harness) { h.tt.Key(0, ui.KeyEscape) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, th, _ := credentialing(t, stripeKey)
			tc.do(h)
			finished(t, h, th)
			if !h.tt.HasText("Declined") || h.tt.HasText("Forget") {
				t.Errorf("the declined card shows %q", h.tt.Texts())
			}
			if got := texts(entriesOf(th)); strings.Contains(got, "STRIPE_KEY is") {
				t.Errorf("a declined agent read a value: %q", got)
			}
		})
	}
}

// Escape in a field leaves the card waiting.
func TestCredentialsEscapeInAFieldDoesNotDecline(t *testing.T) {
	h, th, _ := credentialing(t, stripeKey)
	typeInto(t, h, "Stripe test key", "probe")
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Advance(time.Second)
	if r := lastRequest(th); r.State != api.RequestPending {
		t.Errorf("Escape in the field answered the card: %v", r.State)
	}
}

// Forget deletes what the thread's vault holds.
func TestCredentialsForget(t *testing.T) {
	h, th, dir := credentialing(t, stripeKey)
	typeInto(t, h, "Stripe test key", probeValue)
	h.click("Provide")
	finished(t, h, th)
	held := filepath.Join(dir, "secrets", th.id().String(), "STRIPE_KEY")
	if _, err := os.Stat(held); err != nil {
		t.Fatalf("the vault holds no STRIPE_KEY: %v", err)
	}
	h.click("Forget")
	h.until("Forgotten", func() bool { return h.tt.HasText("Forgotten") })
	if _, err := os.Stat(held); !os.IsNotExist(err) {
		t.Errorf("STRIPE_KEY is still held after Forget: %v", err)
	}
}

// The credentials card names no access level, whatever its turn runs at:
// it asks for a value only the user has, which no level answers.
func TestCredentialsCardNamesNoAccess(t *testing.T) {
	for _, level := range []string{accessAsk, accessAuto, accessFull} {
		t.Run(level, func(t *testing.T) {
			h, th, _ := credentialingAt(t, stripeKey, level)
			if turn := th.View.Turn; turn == nil || accessLabel(turn.Access) != level {
				t.Fatalf("the turn runs at %+v, want %s", turn, level)
			}
			card := h.a.drawn[lastRequest(th).Request].card
			if card.W == 0 || card.H == 0 || !h.tt.HasText("Claude asks for credentials") {
				t.Fatalf("the card isn't drawn: %+v", card)
			}
			for _, n := range h.tt.AccessAll() {
				b := n.Bounds
				overlaps := b.X < card.X+card.W && card.X < b.X+b.W && b.Y < card.Y+card.H && card.Y < b.Y+b.H
				if _, ok := accessOf[n.Name]; ok && overlaps {
					t.Errorf("the card names %q at %+v", n.Name, b)
				}
			}
		})
	}
}

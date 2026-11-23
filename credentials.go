package main

import (
	"cmp"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/plain"
)

// credentialsForm is what the user typed on a credentials card that waits:
// a value per field, and whether each shows. It is dropped the moment the
// values go to the engine, the card is declined, or it stops waiting, and
// nothing else keeps them.
type credentialsForm struct {
	values  []string
	visible []bool
	fields  []ui.Handle
	saving  bool
	err     string
}

// credentialsFormOf is the form on the card of r, kept by request while it
// waits.
func (a *app) credentialsFormOf(r *client.RequestEntry, c api.Credentials) *credentialsForm {
	f := a.credentials[r.Request]
	if f == nil {
		n := len(c.Fields)
		f = &credentialsForm{values: make([]string, n), visible: make([]bool, n), fields: make([]ui.Handle, n)}
		a.credentials[r.Request] = f
	}
	return f
}

// done is whether each field has a value.
func (f *credentialsForm) done() bool {
	return !slices.ContainsFunc(f.values, func(v string) bool { return strings.TrimSpace(v) == "" })
}

// credentialsCard is a credentials request in the timeline: the agent's
// reason and a masked field per value while it waits; which names were
// provided once it is answered; or that it is no longer asked. Values never
// show here once given, only the names.
func (a *app) credentialsCard(c *ui.Context, t thread, e entry) {
	r := e.request
	q, err := api.ParseCredentials(r.Body)
	waiting := r.State == api.RequestPending && err == nil
	if !waiting {
		delete(a.credentials, r.Request)
	}
	escaped := false
	shown := func(s string, lines bool) string {
		s, e := plain.Text(s, lines)
		escaped = escaped || e
		return s
	}
	keyed := a.keyed == r.Request
	provided, _ := api.ParseProvided(r.Answer)
	card := ui.Column(c).Key(e.key).Radius(12).Border(1, palette.border).Background(palette.surface).Clip().
		Transition(riseIn)
	card.Children(func() {
		header := ui.Row(c).Padding(9, 14).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(palette.border)
		header.Children(func() {
			switch {
			case waiting || r.State == api.RequestAnswered:
				a.pendingMark(c)
				ui.Text(c, agentName(t)+" asks for credentials").FontSize(13).FontWeight(600)
			case r.State == api.RequestDelivered && len(provided.Names) > 0:
				ui.Icon(c, icCheck).FontSize(13).TextColor(palette.green)
				ui.Text(c, "Provided").FontSize(13).FontWeight(600)
			case r.State == api.RequestDelivered:
				ui.Icon(c, icX).FontSize(13).TextColor(palette.muted)
				ui.Text(c, "Declined").FontSize(13).FontWeight(600).TextColor(palette.muted)
			default:
				ui.Icon(c, icArrowRight).FontSize(13).TextColor(palette.muted)
				ui.Text(c, "No longer asked").FontSize(13).FontWeight(600).TextColor(palette.muted)
			}
			ui.Spacer(c)
			if waiting && !r.Opened.IsZero() {
				ui.Text(c, "waiting "+ago(r.Opened, a.now)).FontSize(11.5).TextColor(palette.faint)
			}
		})
		var lead ui.Rect
		switch {
		case err != nil:
			ui.Column(c).Padding(12, 14, 12).Gap(8).Children(func() {
				lead = ui.Text(c, agentName(t)+" asks:").FontSize(12.5).TextColor(palette.muted).Bounds()
				ui.Text(c, shown(string(r.Body), false)).Selectable().FontSize(12).Font("monospace")
			})
		case waiting:
			lead = a.credentialsFields(c, r, q, shown)
		default:
			a.credentialsSummary(c, t, r, q, provided, shown)
		}
		if waiting {
			a.drawn[r.Request] = sight{header: header.Bounds(), card: card.Bounds(), lead: lead, texts: card.Bounds()}
		}
		if escaped {
			ui.Box(c).Key("ascii").Padding(0, 14, 12).Children(func() { asciiNotice(c) })
		}
		if r.State != api.RequestPending {
			return
		}
		ui.Row(c).Key("decide").Padding(0, 14, 12).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
			f := a.credentials[r.Request]
			sent, declining := responding(t, r.Request)
			switch {
			case declining && sent.Err != nil:
				ui.Text(c, "Not sent: "+sent.Err.Error()).FontSize(12).TextColor(palette.red).Grow(1).Shrink(1)
				a.failedActions(c, t, sent)
			case declining:
				ui.Text(c, "Sending…").FontSize(12).TextColor(palette.muted)
			case f != nil && f.saving:
				ui.Text(c, "Saving…").FontSize(12).TextColor(palette.muted)
			default:
				a.credentialsButtons(c, t, r, q, keyed)
			}
		})
	})
}

// credentialsFields lays out a waiting card's reason and one masked field
// per value, and returns where the reason's line was drawn.
func (a *app) credentialsFields(c *ui.Context, r *client.RequestEntry, q api.Credentials, shown func(string, bool) string) (lead ui.Rect) {
	f := a.credentialsFormOf(r, q)
	ui.Column(c).Padding(12, 14, 12).Gap(12).Children(func() {
		lead = ui.Text(c, shown(q.Reason, true)).FontSize(13).Shrink(1).Bounds()
		for i, x := range q.Fields {
			ui.Column(c).Key(x.Name).Gap(5).Children(func() {
				ui.Row(c).Gap(7).AlignItems(ui.Center).Wrap().Children(func() {
					ui.Text(c, shown(cmp.Or(x.Label, x.Name), false)).FontSize(12.5).FontWeight(600)
					if x.Label != "" {
						ui.Text(c, x.Name).FontSize(11).Font("monospace").TextColor(palette.faint)
					}
					ui.Spacer(c)
					if x.URL != "" {
						ui.Link(c, "Get it", x.URL).FontSize(11.5)
					}
				})
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					in := ui.TextInput(c.Key("value"), &f.values[i]).Bind(&f.fields[i]).Placeholder("Paste " + x.Name).
						Label(cmp.Or(x.Label, x.Name)).FontSize(12.5).Font("monospace").Grow(1).Disabled(f.saving)
					if !f.visible[i] {
						in.Password()
					}
					eye := ghost(c, "eye", f.visible[i]).Padding(4, 6).Label(map[bool]string{false: "Show " + x.Name, true: "Hide " + x.Name}[f.visible[i]]).
						Children(func() { ui.Icon(c, icEye).FontSize(13).TextColor(palette.muted) })
					if eye.Clicked() {
						f.visible[i] = !f.visible[i]
					}
				})
			})
		}
		where := "Kept for this thread on its machine. The agent's commands can read them; the chat never shows them."
		if q.EnvFile != "" {
			where += " Also written to " + shown(q.EnvFile, false) + "."
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icLockOpen).FontSize(12).TextColor(palette.faint)
			ui.Text(c, where).FontSize(11.5).TextColor(palette.faint).Shrink(1)
		})
		if f.err != "" {
			ui.Text(c, "Not saved: "+f.err).FontSize(12).TextColor(palette.red)
		}
	})
	return lead
}

// credentialsButtons are a waiting card's Provide, once each field has a
// value, and Decline, with their keys while keyed.
func (a *app) credentialsButtons(c *ui.Context, t thread, r *client.RequestEntry, q api.Credentials, keyed bool) {
	f := a.credentialsFormOf(r, q)
	provide := filled(c, "provide", palette.accent, ui.Hex("#ffffff")).Disabled(!f.done()).Children(func() {
		ui.Text(c, "Provide").FontSize(12.5).FontWeight(500)
		if keyed {
			ui.Text(c, "⌘⏎").FontSize(10.5).Padding(1, 4).Radius(4).Background(ui.Hex("#ffffff").Alpha(0.2))
		}
	})
	if !f.done() {
		provide.Opacity(0.5)
	}
	decline := ghost(c, "decline", false).Padding(5, 10).Children(func() {
		ui.Text(c, "Decline").FontSize(12.5)
		if keyed {
			kbd(c, "⇧⌘D")
		}
	})
	ui.Spacer(c)
	ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icHand).FontSize(12).TextColor(palette.faint)
		ui.Text(c, askedAt(t, r)).FontSize(11.5).TextColor(palette.faint)
	})
	if provide.Clicked() {
		a.provideCredentials(t, r, q)
	}
	if decline.Clicked() {
		a.declineCredentials(t, r)
	}
}

// credentialsSummary is what an answered card says: the names provided,
// with Forget while the thread's vault may hold them; or why it's no
// longer asked.
func (a *app) credentialsSummary(c *ui.Context, t thread, r *client.RequestEntry, q api.Credentials, provided api.Provided, shown func(string, bool) string) {
	ui.Column(c).Padding(10, 14, 12).Gap(6).Children(func() {
		ui.Text(c, shown(q.Reason, true)).FontSize(12.5).TextColor(palette.muted).Shrink(1)
		names := provided.Names
		if len(names) == 0 {
			names = q.Names()
		}
		ui.Row(c).Gap(6).Wrap().Children(func() {
			for _, n := range names {
				ui.Text(c, n).FontSize(11.5).Font("monospace").Padding(1, 6).Radius(4).Background(palette.accent.Alpha(0.14))
			}
		})
		switch {
		case r.State == api.RequestAnswered:
			ui.Text(c, "Sending…").FontSize(12).TextColor(palette.muted)
		case r.State == api.RequestInvalidated || r.State == api.RequestExpired:
			ui.Text(c, shown(cmp.Or(r.Reason, "The turn stopped."), true)).FontSize(12).TextColor(palette.faint)
		case r.State == api.RequestDelivered && len(provided.Names) > 0:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				copied := ""
				if q.EnvFile != "" {
					copied = " " + shown(q.EnvFile, false) + " keeps its copy until you delete it."
				}
				if a.forgotten[t.id()] {
					ui.Text(c, "Forgotten: this thread holds no credentials."+copied).FontSize(11.5).TextColor(palette.faint).Shrink(1)
					return
				}
				ui.Text(c, "Kept for this thread."+copied).FontSize(11.5).TextColor(palette.faint).Shrink(1)
				if ghost(c, "forget", false).Padding(3, 8).Children(func() { ui.Text(c, "Forget").FontSize(12) }).Clicked() {
					a.forgetCredentials(t)
				}
			})
		}
	})
}

// provideCredentials sends what the card of r holds to the thread's
// engine, and drops it from the form once it goes.
func (a *app) provideCredentials(t thread, r *client.RequestEntry, q api.Credentials) {
	f := a.credentialsFormOf(r, q)
	if !f.done() || f.saving {
		return
	}
	values := map[string]string{}
	for i, x := range q.Fields {
		values[x.Name] = strings.TrimSpace(f.values[i])
	}
	f.saving, f.err = true, ""
	request, id := r.Request, t.id()
	a.backend.ProvideCredentials(t.env, id, request, values, func(err error) {
		if err == nil {
			delete(a.credentials, request)
			delete(a.forgotten, id)
			return
		}
		if g := a.credentials[request]; g != nil {
			g.saving, g.err = false, err.Error()
		}
	})
	clear(f.values)
}

// declineCredentials answers the card of r with no values.
func (a *app) declineCredentials(t thread, r *client.RequestEntry) {
	if !a.backend.DeclineCredentials(t.env, t.id(), r.Request).IsZero() {
		delete(a.credentials, r.Request)
	}
}

// forgetCredentials deletes what the thread's vault holds.
func (a *app) forgetCredentials(t thread) {
	id := t.id()
	a.backend.ForgetCredentials(t.env, id, func(err error) {
		if err == nil {
			a.forgotten[id] = true
		}
	})
}

// credentialsKeys answer a waiting credentials card from the keyboard, as
// questionKeys do a question's: ⌘⏎ provides once each field has a value,
// and ⇧⌘D or Escape outside its fields declines.
func (a *app) credentialsKeys(c *ui.Context, in ui.Element, t thread, r *client.RequestEntry) {
	enter := c.Shortcut(ui.Cmd, ui.KeyEnter)
	if a.covered {
		return
	}
	shown := a.keyedShown()
	q, err := api.ParseCredentials(r.Body)
	if err != nil {
		if in.OverlayShortcut(ui.Cmd|ui.Shift, ui.KeyD) && shown {
			a.declineCredentials(t, r)
		}
		return
	}
	f := a.credentialsFormOf(r, q)
	typing := slices.ContainsFunc(f.fields, func(h ui.Handle) bool { return h.Focused(c) })
	switch {
	case enter && shown && f.done():
		a.provideCredentials(t, r, q)
	case in.OverlayShortcut(ui.Cmd|ui.Shift, ui.KeyD) && shown || !typing && in.OverlayShortcut(0, ui.KeyEscape):
		a.declineCredentials(t, r)
	}
}

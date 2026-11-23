package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"

	"glitched/internal/api"
	"glitched/internal/client"
)

// sast is the zone the times of these tests are told in, and day the
// day they are told on: Sunday 22 November 2026, 16:05 there.
var (
	sast = time.FixedZone("SAST", 2*60*60)
	day  = time.Date(2026, 11, 22, 16, 5, 0, 0, sast)
)

// stamped is the checks thread with its message sent at sent and its
// reply's words come at came, after a turn of ran, told at day in sast,
// in the formats of region.
func stamped(t *testing.T, region i18n.Region, sent, came time.Time, ran time.Duration) *harness {
	t.Helper()
	h, th := checked(t)
	h.a.clock, h.a.zone = func() time.Time { return day }, sast
	h.tt.SetRegion(region)
	for _, e := range th.Transcript.Entries {
		switch e := e.(type) {
		case *client.UserEntry:
			e.At = sent
		case *client.AssistantEntry:
			e.At = came
			if e.End != nil {
				e.End.Took = ran
			}
		}
	}
	h.a.changed()
	h.tt.Advance(time.Second)
	return h
}

// times are the texts shown that tell a time.
func times(h *harness) []string {
	var out []string
	for _, s := range h.tt.Texts() {
		if strings.ContainsAny(s, "0123456789") && strings.ContainsAny(s, ":.") {
			out = append(out, s)
		}
	}
	return out
}

var (
	britain = i18n.Region{Locale: "en-GB", FirstWeekday: time.Monday, HourCycle: 24}
	states  = i18n.Region{Locale: "en-US", FirstWeekday: time.Sunday, HourCycle: 12}
)

// A message's toolbar says when it was sent, and a reply's when its words
// came and how long the turn ran: the time alone today, the day too
// before, the year too in another year, in the user's formats and hours.
// Its tooltip, which assistive technology reads too, writes the day out.
func TestAMessagesToolbarSaysWhenItWasSent(t *testing.T) {
	const msg, reply = "Sort them by most recent activity.", "One test fails."
	for _, tc := range []struct {
		name       string
		region     i18n.Region
		sent, came time.Time
		ran        time.Duration
		user, said string // what the toolbars say
		full       string // the message's, written out
	}{
		{"today", britain, time.Date(2026, 11, 22, 14, 32, 0, 0, sast), time.Date(2026, 11, 22, 14, 32, 9, 0, sast), 72 * time.Second,
			"14:32", "14:32 · 1m12s", "Sunday, 22 November 2026 at 14:32"},
		{"today in 12 hours", states, time.Date(2026, 11, 22, 14, 32, 0, 0, sast), time.Date(2026, 11, 22, 14, 33, 0, 0, sast), 0,
			"2:32 PM", "2:33 PM", "Sunday, November 22, 2026 at 2:32 PM"},
		{"yesterday", britain, time.Date(2026, 11, 21, 9, 5, 0, 0, sast), time.Date(2026, 11, 22, 0, 1, 0, 0, sast), 56 * time.Minute,
			"21 Nov, 09:05", "00:01 · 56m0s", "Saturday, 21 November 2026 at 09:05"},
		{"last year", britain, time.Date(2025, 12, 31, 23, 59, 0, 0, sast), time.Date(2025, 12, 31, 23, 59, 30, 0, sast), 0,
			"31 Dec 2025, 23:59", "31 Dec 2025, 23:59", "Wednesday, 31 December 2025 at 23:59"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := stamped(t, tc.region, tc.sent, tc.came, tc.ran)
			pointAt(t, h, msg)
			if !h.tt.HasText(tc.user) {
				t.Errorf("your message's toolbar says %q, want %q", times(h), tc.user)
			}
			if r, ok := h.tt.Find(tc.user); ok {
				h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
				h.tt.Advance(2 * time.Second)
			}
			if !h.tt.HasText(tc.full) {
				t.Errorf("the time's tooltip says %q, want %q", times(h), tc.full)
			}
			pointAt(t, h, reply)
			if !h.tt.HasText(tc.said) {
				t.Errorf("the reply's toolbar says %q, want %q", times(h), tc.said)
			}
		})
	}
}

// A message has its time only once it is sent: one still sending has no
// time.
func TestAMessageNotSentYetHasNoTime(t *testing.T) {
	h := stamped(t, britain, time.Time{}, time.Date(2026, 11, 22, 14, 32, 0, 0, sast), 0)
	pointAt(t, h, "Sort them by most recent activity.")
	if !h.tt.HasText("Edit & resend") || len(times(h)) != 0 {
		t.Errorf("an unsent message's toolbar tells %q", times(h))
	}
}

// twoDays is a thread of two turns, the first on Saturday 21 November,
// the other on Sunday 22, told on the Sunday.
func twoDays(t *testing.T, region i18n.Region) *harness {
	t.Helper()
	h, th := checked(t)
	h.a.clock, h.a.zone = func() time.Time { return day }, sast
	h.tt.SetRegion(region)
	turn := func(asked, said string, sent time.Time, ran time.Duration) []client.Entry {
		run := api.NewID()
		end := sent.Add(ran)
		return []client.Entry{
			&client.UserEntry{Message: run, Text: asked, State: client.Sent, At: sent},
			&client.AssistantEntry{Run: run, Text: said, At: sent.Add(4 * time.Second), End: &client.TurnEnd{Outcome: api.OutcomeCompleted, At: end, Took: ran}},
		}
	}
	th.Transcript.Entries = slices.Concat(
		turn("Sort the sidebar's threads by recent activity.", "Done: the sidebar sorts by updatedAt, newest first.", time.Date(2026, 11, 21, 18, 40, 0, 0, sast), 95*time.Second),
		turn("Now pin the active thread to the top.", "Pinned: the thread you have open stays first.", time.Date(2026, 11, 22, 9, 12, 0, 0, sast), 41*time.Second),
	)
	h.a.changed()
	h.tt.Advance(time.Second)
	return h
}

// In a thread that spans two days, the first day's messages tell their
// day and the second's, today, their time alone.
func TestAThreadOfTwoDaysTellsEachDay(t *testing.T) {
	h := twoDays(t, britain)
	for at, want := range map[string]string{
		"Sort the sidebar's threads by recent activity.":      "21 Nov, 18:40",
		"Done: the sidebar sorts by updatedAt, newest first.": "21 Nov, 18:40 · 1m35s",
		"Now pin the active thread to the top.":               "09:12",
		"Pinned: the thread you have open stays first.":       "09:12 · 41s",
	} {
		pointAt(t, h, at)
		if !slices.Contains(times(h), want) {
			t.Errorf("pointing at %q tells %q, want %q", at, times(h), want)
		}
	}
}

// A copy of the toolbar, and of a timeline row, is told when the message
// was sent, and a reply's how long its turn ran, to place and write as
// it likes.
func TestYourToolbarIsToldWhenTheMessageWasSent(t *testing.T) {
	h := stamped(t, britain, time.Date(2026, 11, 22, 14, 32, 0, 0, sast), time.Date(2026, 11, 22, 14, 32, 9, 0, sast), 72*time.Second)
	writeOwnPlugin(t, h.a, "my-bar", barManifest, barModule(`{ type: "row", children: [{ type: "text", text: "Sent " + context.at + " took " + (context.took ?? "none") }] }`))
	allow(t, h.a, "my-bar", "ui.replace")
	pointAt(t, h, "Sort them by most recent activity.")
	if !h.tt.HasText("Sent 2026-11-22T14:32:00+02:00 took none") {
		t.Errorf("your message's toolbar was told %q", h.tt.Texts())
	}
	pointAt(t, h, "One test fails.")
	if !h.tt.HasText("Sent 2026-11-22T14:32:09+02:00 took 72000") {
		t.Errorf("the reply's toolbar was told %q", h.tt.Texts())
	}
}

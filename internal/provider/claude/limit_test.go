package claude

import (
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// A turn a usage limit failed says so, and when the limit resets where
// Claude Code said; a failure no limit caused, or a refusal the account's
// overage carries, says nothing of one.
func TestATurnAUsageLimitFailedSaysWhenItResets(t *testing.T) {
	const (
		blocked  = `{"type":"assistant","error":"rate_limit","message":{"model":"<synthetic>","content":[{"type":"text","text":"You've hit your limit"}]}}`
		limited  = `{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"You've hit your limit"}`
		failed   = `{"type":"result","subtype":"success","is_error":true,"api_error_status":500,"result":"Overloaded"}`
		finished = `{"type":"result","subtype":"success","is_error":false,"result":"done"}`
	)
	reject := func(window string) string {
		return `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected",` + window + `"rateLimitType":"five_hour"}}`
	}
	at := func(s int64) *api.UsageLimit { return &api.UsageLimit{Resets: time.Unix(s, 0).UTC()} }
	for name, c := range map[string]struct {
		frames []string
		want   *api.UsageLimit
	}{
		"a window with its reset":    {[]string{reject(`"resetsAt":1786985400,`), blocked, limited}, at(1786985400)},
		"the later of two windows":   {[]string{reject(`"resetsAt":1786985400,`), reject(`"resetsAt":1787199153,`), blocked, limited}, at(1787199153)},
		"a window with no reset":     {[]string{reject(``), blocked, limited}, &api.UsageLimit{}},
		"one of two with no reset":   {[]string{reject(`"resetsAt":1786985400,`), reject(``), blocked, limited}, &api.UsageLimit{}},
		"only the synthetic reply":   {[]string{blocked, limited}, &api.UsageLimit{}},
		"overage carries the window": {[]string{reject(`"resetsAt":1786985400,"overageStatus":"allowed",`), failed}, nil},
		"another API error":          {[]string{reject(`"resetsAt":1786985400,`), failed}, nil},
		"a turn that went on anyway": {[]string{reject(`"resetsAt":1786985400,`), finished}, nil},
		"an allowed window":          {[]string{`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1786985400}}`, failed}, nil},
	} {
		tn := newTurn(t.TempDir())
		var end *provider.TurnEnded
		for _, f := range c.frames {
			for _, ev := range tn.read(line(t, f)) {
				if e, ok := ev.(provider.TurnEnded); ok {
					end = &e
				}
			}
		}
		if end == nil {
			t.Fatalf("%s: the turn never ended", name)
		}
		switch {
		case c.want == nil && end.Limit != nil:
			t.Errorf("%s: ended with limit %+v, want none", name, *end.Limit)
		case c.want != nil && (end.Limit == nil || *end.Limit != *c.want):
			t.Errorf("%s: ended with limit %v, want %+v", name, end.Limit, *c.want)
		case c.want != nil && end.Outcome != api.OutcomeFailed:
			t.Errorf("%s: ended %v, want failed", name, end.Outcome)
		}
	}
}

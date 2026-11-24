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

// A 429 that no window Claude Code still calls rejected explains, as one
// per minute, or one in a window the account's overage carries, or one a
// later update allowed again, says no time the limit resets: the engine
// never continues the thread on its own for it. T3 Code still calls one
// with a synthetic rate_limit reply a usage limit, and so does Glitched.
func TestATurnAnOrdinaryRateLimitFailedSaysNoReset(t *testing.T) {
	const (
		rejected  = `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1795521600,"rateLimitType":"five_hour"}}`
		allowed   = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1795521600,"rateLimitType":"five_hour"}}`
		paid      = `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1795521600,"rateLimitType":"five_hour","overageStatus":"allowed_warning"}}`
		assistant = `{"type":"assistant","error":"rate_limit","message":{"model":"<synthetic>","content":[{"type":"text","text":"Rate limit reached for requests per minute"}]}}`
		result    = `{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"terminal_reason":"api_error","result":"Rate limit reached for requests per minute"}`
	)
	for name, frames := range map[string][]string{
		"no window":             {assistant, result},
		"overage carries it":    {paid, assistant, result},
		"allowed again":         {rejected, allowed, assistant, result},
		"overage, no synthetic": {paid, result},
	} {
		tn := newTurn(t.TempDir())
		var end *provider.TurnEnded
		for _, f := range frames {
			for _, ev := range tn.read(line(t, f)) {
				if e, ok := ev.(provider.TurnEnded); ok {
					end = &e
				}
			}
		}
		if end == nil {
			t.Fatalf("%s: no end", name)
		}
		if end.Limit != nil && !end.Limit.Resets.IsZero() {
			t.Errorf("%s: the turn says its limit resets at %v, want no reset", name, end.Limit.Resets)
		}
	}
}

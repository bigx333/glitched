package client

import (
	"context"
	"testing"

	"glitched/internal/api"
)

// shown is the thread's model and access as the composer shows them and
// builds the next configure on: the view's, or those of a configure on
// its way or taken.
func shown(th *Thread) api.ThreadConfigure {
	c := api.ThreadConfigure{Model: th.View.Model, Access: th.View.Access}
	for _, p := range th.Pending {
		if b, ok := p.Body.(api.ThreadConfigure); ok && p.Err == nil {
			c = b
		}
	}
	return c
}

// answered is whether every command sent on the thread has its answer.
func answered(th *Thread) bool {
	for _, p := range th.Pending {
		if !p.sent && p.Err == nil {
			return false
		}
	}
	return true
}

func set(b *bridge, flag *bool) {
	b.mu.Lock()
	*flag = true
	b.mu.Unlock()
}

// settled waits for all sent on the thread to have landed and for the view
// of its last commit, and checks that the thread shows what the engine has
// with nothing left pending.
func settled(t *testing.T, b *bridge, g *gui, th *Thread) api.Thread {
	t.Helper()
	g.until(t, "the answers", func() bool { return answered(th) })
	v, _ := engineView(t, b, th.View.ID)
	g.until(t, "the engine's last view", func() bool { return th.View.Revision == v.Revision })
	if got, want := shown(th), (api.ThreadConfigure{Model: v.Model, Access: v.Access}); got != want || len(th.Pending) != 0 {
		t.Errorf("the thread shows %+v with %d pending, the engine has %+v", got, len(th.Pending), want)
	}
	return v
}

// A thread shows the model and access chosen last from when they're
// chosen, however far their receipts run ahead of the views: the next
// configure builds on them, not on the view held.
func TestAThreadShowsItsLastConfigureUntilTheViewHasIt(t *testing.T) {
	b, _, m, g, e, top := treeFixture(t)
	th := e.Threads[top]
	low := fakeModel
	low.Effort = "low"
	want := api.ThreadConfigure{Model: low, Access: api.AccessFull}
	m.ConfigureThread(e, top, low, api.AccessAsk)
	m.ConfigureThread(e, top, low, api.AccessFull)
	if got := shown(th); got != want {
		t.Fatalf("with its configures on their way the thread shows %+v, want %+v", got, want)
	}
	g.until(t, "the receipts", func() bool { return answered(th) })
	if th.View.Model != fakeModel || th.View.Access != api.AccessAsk {
		t.Fatalf("the held shell sent a view: %+v at %v", th.View.Model, th.View.Access)
	}
	if got := shown(th); got != want {
		t.Errorf("with its receipts ahead of its views the thread shows %+v, want %+v", got, want)
	}
	close(b.hold)
	if v := settled(t, b, g, th); v.Model != low || v.Access != api.AccessFull {
		t.Errorf("the engine has %+v at %v, want %+v", v.Model, v.Access, want)
	}
}

// Widened to Full and put back to Ask before a view of either comes, the
// thread shows Ask, as the engine has it, and not the Full its view never
// showed.
func TestAConfigureBackToTheViewsOwnIsWhatTheThreadShows(t *testing.T) {
	b, _, m, g, e, top := treeFixture(t)
	th := e.Threads[top]
	m.ConfigureThread(e, top, fakeModel, api.AccessFull)
	m.ConfigureThread(e, top, fakeModel, api.AccessAsk)
	g.until(t, "the receipts", func() bool { return answered(th) })
	if th.View.Revision != 1 {
		t.Fatalf("the held shell sent a view, revision %d", th.View.Revision)
	}
	if got := shown(th).Access; got != api.AccessAsk {
		t.Errorf("with both receipts in, the thread shows access %v, want the Ask chosen last", got)
	}
	close(b.hold)
	if v := settled(t, b, g, th); v.Access != api.AccessAsk {
		t.Errorf("the engine has access %v, want Ask", v.Access)
	}
}

// A configure that fails with another chosen after it leaves no row: its
// Retry would undo the choice made after it. So it is whether the network
// lost it on the way or lost its answer.
func TestANewerConfigureLeavesNoneBeforeItToRetry(t *testing.T) {
	for name, lost := range map[string]func(*bridge) *bool{
		"lost on the way": func(b *bridge) *bool { return &b.fail },
		"its answer lost": func(b *bridge) *bool { return &b.lose },
	} {
		t.Run(name, func(t *testing.T) {
			b, _, m, g, e, top := treeFixture(t)
			th := e.Threads[top]
			low := fakeModel
			low.Effort = "low"
			set(b, lost(b))
			first := m.ConfigureThread(e, top, fakeModel, api.AccessFull)
			m.ConfigureThread(e, top, low, api.AccessAsk)
			g.until(t, "the answers", func() bool { return answered(th) })
			close(b.hold)
			settled(t, b, g, th)

			// The row's Retry, had it one, then a change of the effort
			// alone, built on what the thread shows.
			m.Retry(e, top, first)
			high := shown(th)
			high.Model.Effort = "high"
			m.ConfigureThread(e, top, high.Model, high.Access)
			if v := settled(t, b, g, th); v.Model != high.Model || v.Access != api.AccessAsk {
				t.Errorf("the engine has %+v at %v, want the effort chosen last at the access chosen last, Ask", v.Model, v.Access)
			}
		})
	}
}

// A configure the engine took though its answer was lost shows as failed.
// Retried once the thread has moved on, as another client's configure
// moves it, it goes with its stored receipt: the thread shows the engine's
// model and access, not the configure's.
func TestARetriedConfigureTheThreadMovedPastGoesWithItsReceipt(t *testing.T) {
	b, _, m, g, e, top := treeFixture(t)
	th := e.Threads[top]
	set(b, &b.lose)
	full := m.ConfigureThread(e, top, fakeModel, api.AccessFull)
	g.until(t, "the failure", func() bool { return len(th.Pending) == 1 && th.Pending[0].Err != nil })
	close(b.hold)
	g.until(t, "the view the engine sent all the same", func() bool { return th.View.Access == api.AccessFull })
	if _, err := b.Endpoint.Submit(context.Background(), api.Command{ID: api.NewID(), Target: top,
		Body: api.ThreadConfigure{Model: fakeModel, Access: api.AccessAsk}}); err != nil {
		t.Fatal(err)
	}
	g.until(t, "the other client's configure", func() bool { return th.View.Access == api.AccessAsk })
	m.Retry(e, top, full)
	g.until(t, "the retry's answer", func() bool { return answered(th) })
	// Any view of the thread after: the configure waits for none.
	m.Rename(e, top, "renamed")
	if v := settled(t, b, g, th); v.Access != api.AccessAsk || v.Title != "renamed" {
		t.Errorf("the engine has %q at access %v, want the rename and the other client's Ask", v.Title, v.Access)
	}
}

// A configure that fails leaves the thread at the one the engine took
// before it, though the view that has it hasn't come: not at the view
// held, where the next configure would be built and undo the one taken.
func TestAFailedConfigureLeavesTheThreadAtTheOneTheEngineTook(t *testing.T) {
	for name, wait := range map[string]bool{
		"taken before the next was chosen":    true,
		"on its way when the next was chosen": false,
	} {
		t.Run(name, func(t *testing.T) {
			b, _, m, g, e, top := treeFixture(t)
			th := e.Threads[top]
			low := fakeModel
			low.Effort = "low"
			taken := api.ThreadConfigure{Model: low, Access: api.AccessAsk}
			m.ConfigureThread(e, top, low, api.AccessAsk)
			if wait {
				g.until(t, "the receipt", func() bool { return answered(th) })
			}
			// The engine refuses this one: a thread keeps its provider.
			refused := m.ConfigureThread(e, top, api.ModelRef{Provider: "other", Model: "other"}, api.AccessAsk)
			g.until(t, "the refusal", func() bool {
				n := len(th.Pending)
				return answered(th) && n > 0 && th.Pending[n-1].Command == refused && th.Pending[n-1].Err != nil
			})
			if th.View.Revision != 1 {
				t.Fatalf("the held shell sent a view, revision %d", th.View.Revision)
			}
			if got := shown(th); got != taken {
				t.Fatalf("with the next configure refused the thread shows %+v, want the one the engine took, %+v", got, taken)
			}
			// A change of the access alone, built on what the thread shows.
			m.ConfigureThread(e, top, shown(th).Model, api.AccessFull)
			close(b.hold)
			if v := settled(t, b, g, th); v.Model != low || v.Access != api.AccessFull {
				t.Errorf("the engine has %+v at %v, want the effort it took at the access chosen after", v.Model, v.Access)
			}
		})
	}
}

// A configure that failed goes again when the user chooses it again, the
// same as before: nothing holds it back as sent already.
func TestAFailedConfigureChosenAgainIsSentAgain(t *testing.T) {
	b, _, m, g, e, top := treeFixture(t)
	close(b.hold)
	th := e.Threads[top]
	low := fakeModel
	low.Effort = "low"
	set(b, &b.fail)
	first := m.ConfigureThread(e, top, low, api.AccessAsk)
	g.until(t, "the failure", func() bool { return len(th.Pending) == 1 && th.Pending[0].Err != nil })
	if again := m.ConfigureThread(e, top, low, api.AccessAsk); again == first {
		t.Fatal("the configure chosen again wasn't sent: it got the failed one's command")
	}
	if v := settled(t, b, g, th); v.Model != low {
		t.Errorf("the engine has %+v, want %+v", v.Model, low)
	}
}

// taking is a thread whose view is at revision 4 on log, with a configure
// to Full on its way.
func taking(log api.ID) (*Manager, *Env, *Thread, api.Command) {
	id := api.NewID()
	full := api.ThreadConfigure{Access: api.AccessFull}
	cmd := api.Command{ID: api.NewID(), Target: id, Body: full}
	th := &Thread{View: api.Thread{ID: id, Revision: 4}, Pending: []Pending{{Command: cmd.ID, Body: full}}, known: true}
	return &Manager{}, &Env{Threads: map[api.ID]*Thread{id: th}, log: log}, th, cmd
}

// A configure's receipt says which view has it: the thread's at the
// receipt's revision. One ahead of the view held leaves the configure to
// wait for that view. One the view is at already, or past, as after
// another client's change, takes the configure with it. A receipt of
// another log than the views held, or one stored without its revision,
// names no view to wait for.
func TestAConfiguresReceiptSaysWhichViewHasIt(t *testing.T) {
	held, other := api.NewID(), api.NewID()
	for name, tc := range map[string]struct {
		receipt api.Receipt
		waits   bool
	}{
		"ahead of the view":          {api.Receipt{Committed: api.Cursor{Log: held}, Revision: 5}, true},
		"at the view's revision":     {api.Receipt{Committed: api.Cursor{Log: held}, Revision: 4}, false},
		"behind the view":            {api.Receipt{Committed: api.Cursor{Log: held}, Revision: 3}, false},
		"stored without a revision":  {api.Receipt{Committed: api.Cursor{Log: held}}, false},
		"of another log, far ahead":  {api.Receipt{Committed: api.Cursor{Log: other}, Revision: 9}, false},
		"of another log, far behind": {api.Receipt{Committed: api.Cursor{Log: other}, Revision: 1}, false},
	} {
		t.Run(name, func(t *testing.T) {
			m, e, th, cmd := taking(held)
			m.submitted(e, cmd, tc.receipt, nil)
			if waits := shown(th).Access == api.AccessFull; waits != tc.waits || len(th.Pending) == 1 != tc.waits {
				t.Errorf("after the receipt the thread shows access %v with %d pending; want the configure waiting: %v", shown(th).Access, len(th.Pending), tc.waits)
			}
		})
	}
}

// A configure the engine took at revision 6 goes with the first view at
// that revision or past it, a snapshot's too, and not with one short of
// it. A snapshot of another log, as a restored database sends, has none
// of the old log's revisions: the configure goes, whatever revision the
// thread is at there.
func TestATakenConfigureGoesWithTheViewThatHasIt(t *testing.T) {
	held, other := api.NewID(), api.NewID()
	for name, tc := range map[string]struct {
		view     api.Thread
		snapshot api.ID // the log of the snapshot that brings the view, or zero for the stream
		waits    bool
	}{
		"a view short of it":                 {api.Thread{Revision: 5}, api.ID{}, true},
		"the view at it":                     {api.Thread{Revision: 6, Access: api.AccessFull}, api.ID{}, false},
		"a view past it, the thread moved":   {api.Thread{Revision: 7, Access: api.AccessAutoEdits}, api.ID{}, false},
		"a snapshot at it":                   {api.Thread{Revision: 6, Access: api.AccessFull}, held, false},
		"a snapshot of another log, behind":  {api.Thread{Revision: 2}, other, false},
		"a snapshot of another log, past it": {api.Thread{Revision: 8}, other, false},
	} {
		t.Run(name, func(t *testing.T) {
			m, e, th, cmd := taking(held)
			m.submitted(e, cmd, api.Receipt{Committed: api.Cursor{Log: held}, Revision: 6}, nil)
			if len(th.Pending) != 1 {
				t.Fatalf("a configure whose receipt is ahead of the view didn't wait: %+v", th.Pending)
			}
			tc.view.ID = th.View.ID
			if tc.snapshot.IsZero() {
				m.view(e, tc.view)
			} else {
				snap := api.Snapshot{Cursor: api.Cursor{Log: tc.snapshot}, Threads: []api.Thread{tc.view}}
				m.shell(e, nil, api.Environment{}, api.StreamItem{Kind: api.StreamSnapshot, Snapshot: &snap})
			}
			want := tc.view.Access
			if tc.waits {
				want = api.AccessFull
			}
			if got := shown(th).Access; got != want || len(th.Pending) == 1 != tc.waits {
				t.Errorf("the thread shows access %v with %d pending; want %v, the configure waiting: %v", got, len(th.Pending), want, tc.waits)
			}
		})
	}
}

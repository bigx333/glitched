package client

import (
	"slices"
	"testing"

	"glitched/internal/api"
)

func TestEditMarksAcrossTheEditsKept(t *testing.T) {
	run := api.NewID()
	left := api.WorkItem{Run: run, Item: "left", Output: "left"}
	right := api.WorkItem{Run: run, Item: "right", Output: "right"}
	tr := play(left, api.AssistantDelta{Run: run, Text: "separator"}, right)
	apply := func(item api.WorkItem, output string) {
		item.Output = output
		tr.apply(event(int(tr.Cursor.Seq)+1, item))
	}
	_, _, mark := tr.Edits(EditMark{})
	apply(left, "before read, first")
	apply(left, "before read, second")
	edits, all, read := tr.Edits(mark)
	if all || !slices.Equal(edits, []int{0}) || read.n != mark.n+1 {
		t.Fatalf("before the first read: indexes=%v all=%t next=%v after mark %v", edits, all, read, mark)
	}
	apply(left, "after read")
	edits, all, next := tr.Edits(read)
	if all || !slices.Equal(edits, []int{0}) || next.n != read.n+1 {
		t.Fatalf("after the first read: indexes=%v all=%t next=%v after mark %v", edits, all, next, read)
	}
	for range editsKept {
		apply(right, "continued")
		apply(left, "continued")
	}
	edits, all, latest := tr.Edits(next)
	if !all || len(edits) != 0 || latest.n != next.n+2*editsKept {
		t.Fatalf("lagging reader: index count=%d all=%t next=%v, want %d", len(edits), all, latest, next.n+2*editsKept)
	}
	edits, all, latest = tr.Edits(EditMark{latest.folds, latest.n - 1})
	if all || !slices.Equal(edits, []int{0}) {
		t.Fatalf("recent reader: indexes=%v all=%t", edits, all)
	}
	apply(left, "new after wrap")
	edits, all, next = tr.Edits(latest)
	if all || !slices.Equal(edits, []int{0}) || next.n != latest.n+1 {
		t.Fatalf("next edit after wrap: indexes=%v all=%t next=%v after mark %v", edits, all, next, latest)
	}
}

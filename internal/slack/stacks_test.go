package slack

import (
	"reflect"
	"testing"

	"github.com/voriteam/pull-request-notifier/internal/github"
)

func pr(n int, repo, head, base string) github.TrackedPR {
	return github.TrackedPR{Number: n, Repo: repo, HeadRef: head, BaseRef: base}
}

type shape struct{ number, depth, size int }

func shapes(stacks [][]stackRow) [][]shape {
	var out [][]shape
	for _, s := range stacks {
		var rows []shape
		for _, r := range s {
			rows = append(rows, shape{r.pr.Number, r.depth, r.size})
		}
		out = append(out, rows)
	}
	return out
}

func TestGroupStacks(t *testing.T) {
	prs := []github.TrackedPR{
		pr(3, "o/a", "c", "b"), // top of a chain, listed first
		pr(9, "o/a", "solo", "main"),
		pr(1, "o/a", "a", "main"),
		pr(2, "o/a", "b", "a"),
		pr(4, "o/a", "b2", "a"), // sibling of #2
		pr(7, "o/b", "b", "a"),  // same branch names in another repo
	}
	want := [][]shape{
		{{1, 0, 4}, {2, 1, 0}, {3, 2, 0}, {4, 1, 0}},
		{{9, 0, 1}},
		{{7, 0, 1}},
	}
	if got := shapes(groupStacks(prs)); !reflect.DeepEqual(got, want) {
		t.Errorf("groupStacks = %v, want %v", got, want)
	}
}

func TestGroupStacks_RootFirstMemberOrder(t *testing.T) {
	prs := []github.TrackedPR{pr(2, "o/a", "b", "a"), pr(5, "o/a", "x", "main"), pr(1, "o/a", "a", "main")}
	want := [][]shape{{{1, 0, 2}, {2, 1, 0}}, {{5, 0, 1}}}
	if got := shapes(groupStacks(prs)); !reflect.DeepEqual(got, want) {
		t.Errorf("groupStacks = %v, want %v", got, want)
	}
}

func TestSplitReady_KeepsStacksWhole(t *testing.T) {
	ok := func(p github.TrackedPR) github.TrackedPR { p.CheckState = "SUCCESS"; return p }
	prs := []github.TrackedPR{
		ok(pr(1, "o/a", "a", "main")), pr(2, "o/a", "b", "a"), // #2 not ready
		ok(pr(5, "o/a", "x", "main")), ok(pr(6, "o/a", "y", "x")),
	}
	ready, others := splitReady(groupStacks(prs))
	var r, o []int
	for _, row := range ready {
		r = append(r, row.pr.Number)
	}
	for _, row := range others {
		o = append(o, row.pr.Number)
	}
	if !reflect.DeepEqual(r, []int{5, 6}) || !reflect.DeepEqual(o, []int{1, 2}) {
		t.Errorf("ready %v, others %v", r, o)
	}
}

func TestStackRowText(t *testing.T) {
	bottom := stackRowText(stackRow{pr: github.TrackedPR{URL: "u", Title: "Base"}, size: 3}, "meta")
	if bottom != "*<u|Base>*\n⛓ Stack of 3 · meta" {
		t.Errorf("bottom row %q", bottom)
	}
	second := stackRowText(stackRow{pr: github.TrackedPR{URL: "u", Title: "Top"}, depth: 2}, "meta")
	if second != " ↳ *<u|Top>*\n  meta" {
		t.Errorf("depth-2 row %q", second)
	}
	if solo := stackRowText(stackRow{pr: github.TrackedPR{URL: "u", Title: "Solo"}, size: 1}, "meta"); solo != "*<u|Solo>*\nmeta" {
		t.Errorf("solo row %q", solo)
	}
}

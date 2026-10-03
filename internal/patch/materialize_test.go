package patch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/runner"
	"github.com/pushguard/pushguard/internal/testutil"
)

func TestReplacementProposalsBecomeExactDiffsWithoutEditing(t *testing.T) {
	for _, tc := range []struct{ old, new string }{
		{"before\nvalue\nafter\n", "before\nfixed\nafter\n"},
		{"old", "new"}, {"old\n", "old"}, {"old", "old\n"},
		{"old", "old\nnew\n"}, {"old\n", ""},
	} {
		t.Run(tc.old+"->"+tc.new, func(t *testing.T) {
			root, _ := testutil.Repository(t)
			testutil.Write(t, root, "source.txt", tc.old)
			testutil.Commit(t, root)
			p := model.RepairProposal{Edits: []model.TextEdit{{File: "source.txt", OldText: tc.old, NewText: tc.new}}}
			if err := Materialize(root, &p); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(root, "source.txt"))
			if string(before) != tc.old {
				t.Fatal("proposal changed filesystem")
			}
			if _, err := Apply(context.Background(), root, p, p.Files, runner.Runner{}); err != nil {
				t.Fatalf("generated diff rejected: %v\n%s", err, p.Patch)
			}
			after, _ := os.ReadFile(filepath.Join(root, "source.txt"))
			if string(after) != tc.new {
				t.Fatalf("wrong result %q", after)
			}
		})
	}
}

func TestReplacementProposalRejectsAmbiguousAndProtectedEdits(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "source.txt", "duplicate duplicate\n")
	for _, edit := range []model.TextEdit{
		{File: "source.txt", OldText: "duplicate", NewText: "fixed"},
		{File: "source.txt", OldText: "absent", NewText: "fixed"},
		{File: "source.txt", OldText: "", NewText: "fixed"},
		{File: "../outside", OldText: "old", NewText: "fixed"},
		{File: ".pushguard.json", OldText: "old", NewText: "fixed"},
	} {
		p := model.RepairProposal{Edits: []model.TextEdit{edit}}
		if err := Materialize(root, &p); err == nil {
			t.Fatalf("accepted %+v", edit)
		}
	}
}

func TestReplacementBatchUsesOriginalSource(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "source.txt", "first\nsecond\n")
	// The first replacement introduces a second copy of the next anchor. Both
	// edits still identify unique, disjoint fragments in the original source.
	p := model.RepairProposal{Edits: []model.TextEdit{
		{File: "source.txt", OldText: "first", NewText: "second"},
		{File: "source.txt", OldText: "second", NewText: "third"},
	}}
	if err := Materialize(root, &p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Patch, "+second\n+third\n") {
		t.Fatal(p.Patch)
	}
	data, _ := os.ReadFile(filepath.Join(root, "source.txt"))
	if string(data) != "first\nsecond\n" {
		t.Fatal("materialization wrote source")
	}
}

func TestReplacementBatchRejectsOverlapsAndChainedAnchors(t *testing.T) {
	root := t.TempDir()
	testutil.Write(t, root, "source.txt", "original source\n")
	for _, edits := range [][]model.TextEdit{
		{{File: "source.txt", OldText: "original", NewText: "changed"}, {File: "source.txt", OldText: "changed", NewText: "chained"}},
		{{File: "source.txt", OldText: "original", NewText: "changed"}, {File: "source.txt", OldText: "original source", NewText: "overlap"}},
		{{File: "source.txt", OldText: "original", NewText: "original"}},
	} {
		p := model.RepairProposal{Edits: edits}
		if err := Materialize(root, &p); err == nil {
			t.Fatalf("accepted conflicting batch: %+v", edits)
		}
	}
}

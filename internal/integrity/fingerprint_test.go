package integrity

import (
	"context"
	"testing"

	"github.com/pushguard/pushguard/internal/runner"
	"github.com/pushguard/pushguard/internal/testutil"
)

func TestFingerprintInvalidatesReviewedStateAndConfiguration(t *testing.T) {
	root, _ := testutil.Repository(t)
	testutil.Write(t, root, "value.txt", "reviewed\n")
	testutil.Commit(t, root)
	before, err := Compute(context.Background(), root, "config-a", runner.Runner{})
	if err != nil {
		t.Fatal(err)
	}
	again, err := Compute(context.Background(), root, "config-a", runner.Runner{})
	if err != nil || before.Value != again.Value {
		t.Fatal("stable state changed")
	}
	config, err := Compute(context.Background(), root, "config-b", runner.Runner{})
	if err != nil || config.Value == before.Value {
		t.Fatal("config did not invalidate state")
	}
	testutil.Write(t, root, "value.txt", "unreviewed\n")
	changed, err := Compute(context.Background(), root, "config-a", runner.Runner{})
	if err != nil || changed.Value == before.Value {
		t.Fatal("working tree change did not invalidate state")
	}
	testutil.Commit(t, root)
	committed, err := Compute(context.Background(), root, "config-a", runner.Runner{})
	if err != nil || committed.HEAD == before.HEAD || committed.Value == changed.Value {
		t.Fatal("commit did not invalidate state")
	}
}

package receipt

import (
	"context"
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/runner"
	"github.com/pushguard/pushguard/internal/testutil"
)

func TestHistoricalProvenanceRequiresTrustedVerifiedAncestry(t *testing.T) {
	root, _ := testutil.Repository(t)
	testutil.Write(t, root, "value.txt", "approved repair\n")
	testutil.Commit(t, root)
	v, _, _ := signedFixture(t)
	key, err := SigningKey()
	if err != nil {
		t.Fatal(err)
	}
	v.Branch = "main"
	v.CommitSHA = strings.TrimSpace(testutil.Git(t, root, "rev-parse", "HEAD"))
	v.TreeHash = strings.TrimSpace(testutil.Git(t, root, "rev-parse", "HEAD^{tree}"))
	v.AIRepairs = []AIRepairRecord{{Provider: "test", Files: []string{"value.txt"}, PatchHash: Digest([]byte("approved patch")), HumanApproved: true, VerifiedBy: []string{"tests"}}}
	v.Sign(key)
	if err = SaveVerification(root, v); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, root, "value.txt", "approved repair\nadditional developer change\n")
	testutil.Commit(t, root)
	head := strings.TrimSpace(testutil.Git(t, root, "rev-parse", "HEAD"))
	pub := key.Public().(ed25519.PublicKey)
	if records := priorRepairs(context.Background(), root, v.Repository, "main", head, pub, runner.Runner{}); len(records) != 1 {
		t.Fatal("developer edit erased verified repair history")
	}
	if records := priorRepairs(context.Background(), root, "github.com/other/repo", "main", head, pub, runner.Runner{}); len(records) != 0 {
		t.Fatal("foreign repository provenance used")
	}
	if records := priorRepairs(context.Background(), root, v.Repository, "different-branch", head, pub, runner.Runner{}); len(records) != 0 {
		t.Fatal("foreign branch provenance used")
	}
	testutil.Git(t, root, "switch", "--orphan", "unrelated")
	testutil.Write(t, root, "unrelated.txt", "different history\n")
	testutil.Commit(t, root)
	unrelated := strings.TrimSpace(testutil.Git(t, root, "rev-parse", "HEAD"))
	if records := priorRepairs(context.Background(), root, v.Repository, "main", unrelated, pub, runner.Runner{}); len(records) != 0 {
		t.Fatal("nonancestor provenance used")
	}
}

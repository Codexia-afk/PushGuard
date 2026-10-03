package approval

import "testing"

func TestApprovalIsScopedAndOneUse(t *testing.T) {
	l := &Ledger{}
	a := l.Grant(Apply, "state-a")
	if err := l.Consume(a, Apply, "state-b"); err == nil {
		t.Fatal("stale binding was accepted")
	}
	a = l.Grant(Apply, "state-a")
	if err := l.Consume(a, Apply, "state-a"); err != nil {
		t.Fatal(err)
	}
	if err := l.Consume(a, Apply, "state-a"); err == nil {
		t.Fatal("approval was reusable")
	}
}

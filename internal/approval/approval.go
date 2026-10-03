// Package approval binds each human decision to one exact operation.
package approval

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Scope string

type Decision int

const (
	DecisionUnknown Decision = iota
	DecisionAllow
	DecisionDeny
)

func ParseDecision(input string) Decision {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "y", "yes", "allow", "allow fix", "allow push":
		return DecisionAllow
	case "n", "no", "deny", "stop", "cancel", "x", "exit", "quit":
		return DecisionDeny
	default:
		return DecisionUnknown
	}
}

const (
	Start       Scope = "start_checks"
	Analyze     Scope = "analyze"
	Generate    Scope = "generate_fix"
	Apply       Scope = "apply_patch"
	Continue    Scope = "continue_repair"
	Review      Scope = "review"
	Push        Scope = "push"
	Commit      Scope = "commit_reviewed_repairs"
	PullRequest Scope = "create_or_update_pull_request"
	Publish     Scope = "publish_verification_check"
)

type Approval struct {
	ID      string    `json:"id"`
	Scope   Scope     `json:"scope"`
	Binding string    `json:"binding"`
	At      time.Time `json:"at"`
}

func Bind(value any) string {
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Ledger owns one-use grants. Merely constructing an Approval cannot grant authority.
type Ledger struct {
	mu     sync.Mutex
	grants map[string]Approval
	events []Approval
}

func (l *Ledger) Grant(scope Scope, binding string) Approval {
	l.mu.Lock()
	defer l.mu.Unlock()
	var id [24]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic("secure approval nonce unavailable")
	}
	a := Approval{ID: hex.EncodeToString(id[:]), Scope: scope, Binding: binding, At: time.Now().UTC()}
	if l.grants == nil {
		l.grants = map[string]Approval{}
	}
	l.grants[a.ID] = a
	l.events = append(l.events, a)
	return a
}
func (l *Ledger) Consume(a Approval, scope Scope, binding string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	grant, ok := l.grants[a.ID]
	delete(l.grants, a.ID)
	if !ok || grant != a || a.Scope != scope || a.Binding != binding {
		return fmt.Errorf("missing, reused, or stale %s approval", scope)
	}
	return nil
}
func (l *Ledger) Events() []Approval {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Approval(nil), l.events...)
}

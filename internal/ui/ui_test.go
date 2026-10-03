package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestPromptKeepsBufferedAnswersAcrossQuestions(t *testing.T) {
	in := strings.NewReader("y\ny\n")
	var out bytes.Buffer
	u := New(in, &out, &out, false)
	if !u.Prompt("first", "[Y]") || !u.Prompt("second", "[Y]") {
		t.Fatal("expected both approvals")
	}
}

func TestUnknownEOFAndPartialInputCannotAuthorize(t *testing.T) {
	for _, input := range []string{"", "allow", "yes", "maybe\n", "deny\n", "\n"} {
		var out bytes.Buffer
		if New(strings.NewReader(input), &out, &out, false).Prompt("allow?") {
			t.Fatalf("authorized %q", input)
		}
	}
	var out bytes.Buffer
	if !New(strings.NewReader("allow\n"), &out, &out, false).Prompt("allow?") {
		t.Fatal("explicit allow rejected")
	}
}

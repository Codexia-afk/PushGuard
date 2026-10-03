package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/receipt"
)

func TestPRCLIHelpAndReadOnlyKeyExport(t *testing.T) {
	var out, errors bytes.Buffer
	if code := runWith(context.Background(), []string{"pr", "--help"}, strings.NewReader(""), &out, &errors); code != 0 || !strings.Contains(out.String(), "--") {
		if !strings.Contains(out.String(), "body-file") {
			t.Fatalf("%d %s %s", code, out.String(), errors.String())
		}
	}
	t.Setenv("PUSHGUARD_CACHE_DIR", t.TempDir())
	out.Reset()
	errors.Reset()
	if code := runWith(context.Background(), []string{"pr", "key", "--json"}, strings.NewReader(""), &out, &errors); code != 0 {
		t.Fatal(code, errors.String())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatal(out.String())
	}
	if _, err := receipt.ParseTrust(out.Bytes()); err != nil {
		t.Fatal(err)
	}
}
func TestPRCLIRejectsUnknownCommandsAndMissingRepairNumber(t *testing.T) {
	for _, args := range [][]string{{"pr", "merge"}, {"pr", "repair", "not-a-number"}, {"pr", "--wait", "-1s"}} {
		var out bytes.Buffer
		if code := runWith(context.Background(), args, strings.NewReader("y\n"), &out, &out); code == 0 {
			t.Fatal("invalid invocation accepted", args)
		}
	}
}

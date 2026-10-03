package main

import (
	"bytes"
	"context"
	"testing"
)

func TestHostedCommandDoesNotDefaultToPublishing(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"--repository", "https://evil.example/a/b", "--publish"}, &out, &out); code == 0 {
		t.Fatal("unsafe repository accepted")
	}
	if code := run(context.Background(), []string{"--help"}, &out, &out); code != 0 {
		t.Fatal(code)
	}
}

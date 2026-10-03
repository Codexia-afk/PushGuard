package runner

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCommandHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--helper" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "large":
		fmt.Print(strings.Repeat("x", 3<<20))
	case "sleep":
		time.Sleep(20 * time.Second)
	case "stderr":
		fmt.Fprint(os.Stderr, "actual stderr")
		os.Exit(17)
	case "env":
		fmt.Print(os.Getenv("PUSHGUARD_RUNNER_TEST"))
	}
	os.Exit(0)
}
func TestCaptureLimitTimeoutAndExitCode(t *testing.T) {
	exe, _ := os.Executable()
	for _, mode := range []string{"large", "sleep", "stderr"} {
		t.Run(mode, func(t *testing.T) {
			timeout := 5 * time.Second
			if mode == "sleep" {
				timeout = 100 * time.Millisecond
			}
			r := Runner{}.Run(context.Background(), t.TempDir(), []string{exe, "-test.run=^TestCommandHelper$", "--", "--helper", mode}, timeout)
			switch mode {
			case "large":
				if !r.Truncated || len(r.Stdout) != 2<<20 {
					t.Fatalf("unbounded capture: %+v", r)
				}
			case "sleep":
				if !r.TimedOut || r.ExitCode != 124 || r.Duration > 5*time.Second {
					t.Fatalf("timeout ineffective: %+v", r)
				}
			case "stderr":
				if r.ExitCode != 17 || r.Stderr != "actual stderr" {
					t.Fatalf("exit/log evidence wrong: %+v", r)
				}
			}
		})
	}
}

func TestEnvironmentOverridesAreDeterministic(t *testing.T) {
	exe, _ := os.Executable()
	t.Setenv("PUSHGUARD_RUNNER_TEST", "inherited")
	result := (Runner{Environment: map[string]string{"PUSHGUARD_RUNNER_TEST": "overridden"}}).Run(context.Background(), t.TempDir(), []string{exe, "-test.run=^TestCommandHelper$", "--", "--helper", "env"}, 5*time.Second)
	if result.ExitCode != 0 || result.Stdout != "overridden" {
		t.Fatalf("environment override was not applied: %+v", result)
	}
}

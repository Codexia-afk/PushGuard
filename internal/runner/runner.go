package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/model"
)

type Runner struct {
	Stream      func(string, bool)
	Environment map[string]string
	// Remove lists inherited variables that must not reach the child process.
	Remove []string
}

func (r Runner) Run(ctx context.Context, cwd string, args []string, timeout time.Duration) model.CommandResult {
	return r.RunInput(ctx, cwd, args, timeout, "")
}

func (r Runner) RunInput(ctx context.Context, cwd string, args []string, timeout time.Duration, input string) model.CommandResult {
	started := time.Now()
	result := model.CommandResult{Args: append([]string(nil), args...), Command: strings.Join(args, " "), WorkingDir: cwd, Started: started}
	if len(args) == 0 {
		result.ExitCode = 127
		result.Stderr = "empty command"
		result.Finished = time.Now()
		result.Duration = time.Since(started)
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	program := args[0]
	if program == "git" {
		if path, err := GitPath(); err == nil {
			program = path
		}
	}
	cmd := exec.CommandContext(cmdCtx, program, args[1:]...)
	cmd.Stdin = strings.NewReader(input)
	cmd.WaitDelay = 2 * time.Second
	configureProcess(cmd)
	cmd.Dir = cwd
	overrides := make(map[string]string, len(r.Environment)+2)
	for key, value := range r.Environment {
		overrides[key] = value
	}
	if args[0] == "git" {
		overrides["GIT_TERMINAL_PROMPT"] = "0"
		overrides["GIT_ALLOW_PROTOCOL"] = "file:http:https:ssh:git"
	}
	// On macOS, /usr/bin/git can be an Xcode license shim. Go and other
	// toolchains may invoke Git themselves (for example for build VCS
	// stamping), so expose the same working Git directory to child processes.
	if runtime.GOOS == "darwin" {
		if git, err := GitPath(); err == nil && filepath.Clean(git) != "/usr/bin/git" {
			path := os.Getenv("PATH")
			if configured, ok := r.Environment["PATH"]; ok {
				path = configured
			}
			overrides["PATH"] = filepath.Dir(git) + string(os.PathListSeparator) + path
		}
	}
	cmd.Env = removeEnvironment(mergedEnvironment(os.Environ(), overrides), r.Remove)
	var stdout, stderr bytes.Buffer
	outWriter := &streamWriter{buffer: &stdout, stream: r.Stream}
	errWriter := &streamWriter{buffer: &stderr, stream: r.Stream, stderr: true}
	cmd.Stdout, cmd.Stderr = outWriter, errWriter
	err := cmd.Run()
	result.Stdout, result.Stderr = stdout.String(), stderr.String()
	result.Truncated = outWriter.truncated || errWriter.truncated
	result.Finished = time.Now()
	result.Duration = time.Since(started)
	if cmdCtx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.Terminated = "timeout"
		result.ExitCode = 124
		return result
	}
	if cmdCtx.Err() == context.Canceled {
		result.Terminated = "canceled"
		result.ExitCode = 130
		return result
	}
	if err == nil {
		result.ExitCode = 0
		return result
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
	} else {
		result.ExitCode = 127
		result.Terminated = err.Error()
		if result.Stderr == "" {
			result.Stderr = err.Error()
		}
	}
	return result
}

// mergedEnvironment replaces inherited variables instead of appending duplicate
// entries. Duplicate environment keys have platform-dependent lookup behavior,
// which could otherwise make CI and noninteractive Git settings ineffective.
func mergedEnvironment(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	seen := make(map[string]bool, len(base)+len(overrides))
	canon := func(k string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(k)
		}
		return k
	}
	overrideLookup := func(k string) (string, bool) {
		if v, ok := overrides[k]; ok {
			return v, true
		}
		if runtime.GOOS == "windows" {
			for ok, ov := range overrides {
				if strings.EqualFold(k, ok) {
					return ov, true
				}
			}
		}
		return "", false
	}
	for _, entry := range base {
		key := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			key = entry[:index]
		}
		ck := canon(key)
		if seen[ck] {
			continue
		}
		seen[ck] = true
		if value, ok := overrideLookup(key); ok {
			out = append(out, key+"="+value)
		} else {
			out = append(out, entry)
		}
	}
	for key, value := range overrides {
		ck := canon(key)
		if !seen[ck] {
			seen[ck] = true
			out = append(out, key+"="+value)
		}
	}
	return out
}

func removeEnvironment(env []string, remove []string) []string {
	if len(remove) == 0 {
		return env
	}
	out := env[:0:0]
	for _, entry := range env {
		key := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			key = entry[:index]
		}
		drop := false
		for _, name := range remove {
			if key == name || runtime.GOOS == "windows" && strings.EqualFold(key, name) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, entry)
		}
	}
	return out
}

type streamWriter struct {
	buffer    *bytes.Buffer
	stream    func(string, bool)
	stderr    bool
	truncated bool
}

func (w *streamWriter) Write(p []byte) (int, error) {
	if w.stream != nil {
		w.stream(string(p), w.stderr)
	}
	n := len(p)
	remaining := (2 << 20) - w.buffer.Len()
	if remaining < len(p) {
		w.truncated = true
		p = p[:remaining]
	}
	_, _ = w.buffer.Write(p)
	return n, nil
}
func CommandString(args []string) string {
	if len(args) == 0 {
		return ""
	}
	out := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t\"'") {
			out[i] = fmt.Sprintf("%q", a)
		} else {
			out[i] = a
		}
	}
	return strings.Join(out, " ")
}

// GitPath bypasses the macOS license shim only when a working installed Git exists.
func GitPath() (string, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}
	if runtime.GOOS != "darwin" || path != "/usr/bin/git" {
		return path, nil
	}
	for _, candidate := range []string{"/Library/Developer/CommandLineTools/usr/bin/git", "/Applications/Xcode.app/Contents/Developer/usr/bin/git"} {
		if info, e := os.Stat(candidate); e == nil && !info.IsDir() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			e = exec.CommandContext(ctx, candidate, "--version").Run()
			cancel()
			if e == nil {
				return filepath.Clean(candidate), nil
			}
		}
	}
	return path, nil
}

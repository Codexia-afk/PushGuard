package integrity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/runner"
)

// Compute fails closed: missing Git evidence never becomes a reusable fingerprint.
func Compute(ctx context.Context, root, configHash string, r runner.Runner) (model.StateFingerprint, error) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return model.StateFingerprint{}, err
	}
	root, err = filepath.Abs(canonical)
	if err != nil {
		return model.StateFingerprint{}, err
	}
	return compute(ctx, root, configHash, r, 0)
}
func compute(ctx context.Context, root, configHash string, r runner.Runner, depth int) (model.StateFingerprint, error) {
	if depth > 5 {
		return model.StateFingerprint{}, fmt.Errorf("nested submodule depth exceeds safety limit")
	}
	h := sha256.New()
	write := func(s string) { h.Write([]byte(s)); h.Write([]byte{0}) }
	git := func(args ...string) (string, error) {
		res := r.Run(ctx, root, append([]string{"git"}, args...), 30*time.Second)
		if res.ExitCode != 0 || res.Truncated {
			return "", fmt.Errorf("state evidence unavailable: git %s: %s", strings.Join(args, " "), strings.TrimSpace(res.Stderr+res.Terminated))
		}
		return res.Stdout, nil
	}
	head, err := git("rev-parse", "--verify", "HEAD")
	if err != nil {
		return model.StateFingerprint{}, err
	}
	write(strings.TrimSpace(head))
	write(configHash)
	for _, args := range [][]string{{"rev-parse", "--git-dir"}, {"config", "--local", "--null", "--list"}, {"remote", "-v"}, {"diff", "--binary", "--no-ext-diff"}, {"diff", "--cached", "--binary", "--no-ext-diff"}, {"status", "--porcelain=v1", "-z", "--untracked-files=all"}} {
		output, e := git(args...)
		if e != nil {
			return model.StateFingerprint{}, e
		}
		write(output)
	}
	branch := r.Run(ctx, root, []string{"git", "symbolic-ref", "--short", "-q", "HEAD"}, 10*time.Second)
	if branch.ExitCode != 0 && branch.ExitCode != 1 {
		return model.StateFingerprint{}, fmt.Errorf("branch evidence unavailable")
	}
	write(branch.Stdout)
	output, err := git("ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return model.StateFingerprint{}, err
	}
	paths := append(strings.Split(output, "\x00"), ".pushguard.json", ".pushguard.yaml", ".pushguard.yml")
	if envFiles, e := filepath.Glob(filepath.Join(root, ".env*")); e == nil {
		for _, file := range envFiles {
			if rel, e := filepath.Rel(root, file); e == nil {
				paths = append(paths, rel)
			}
		}
	}
	sort.Strings(paths)
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		full := filepath.Join(root, filepath.FromSlash(path))
		rel, e := filepath.Rel(root, full)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return model.StateFingerprint{}, fmt.Errorf("unsafe state path")
		}
		parent, e := filepath.EvalSymlinks(filepath.Dir(full))
		if e == nil {
			relParent, re := filepath.Rel(root, parent)
			if re != nil || relParent == ".." || strings.HasPrefix(relParent, ".."+string(filepath.Separator)) {
				return model.StateFingerprint{}, fmt.Errorf("state path leaves repository through a symlink")
			}
		}
		write(path)
		info, e := os.Lstat(full)
		if os.IsNotExist(e) {
			write("absent")
			continue
		}
		if e != nil {
			return model.StateFingerprint{}, e
		}
		write(info.Mode().String())
		if info.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(full)
			if e != nil {
				return model.StateFingerprint{}, e
			}
			write(target)
			continue
		}
		if info.IsDir() {
			sub, e := compute(ctx, full, configHash, r, depth+1)
			if e != nil {
				return model.StateFingerprint{}, fmt.Errorf("submodule %s: %w", path, e)
			}
			write(sub.Value)
			continue
		}
		if !info.Mode().IsRegular() {
			return model.StateFingerprint{}, fmt.Errorf("unsupported state file %s", path)
		}
		f, e := os.Open(full)
		if e != nil {
			return model.StateFingerprint{}, e
		}
		_, e = io.Copy(h, f)
		closeErr := f.Close()
		if e != nil {
			return model.StateFingerprint{}, e
		}
		if closeErr != nil {
			return model.StateFingerprint{}, closeErr
		}
		write("")
	}
	return model.StateFingerprint{Value: hex.EncodeToString(h.Sum(nil)), HEAD: strings.TrimSpace(head), CreatedAt: time.Now().UTC(), Description: "HEAD, Git configuration, index, tracked content and modes, untracked content, submodules, and on-disk PushGuard configuration"}, nil
}

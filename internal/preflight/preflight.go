package preflight

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/repository"
	"github.com/pushguard/pushguard/internal/runner"
)

type Service struct{ Runner runner.Runner }

func (s Service) RemoteHEAD(ctx context.Context, root, remote, branch string) (string, error) {
	if remote == "" || strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, "\n\r\x00") || !safeGitArgument(branch) {
		return "", fmt.Errorf("unsafe push target")
	}
	r := s.Runner.Run(ctx, root, []string{"git", "ls-remote", "--heads", "--", remote, "refs/heads/" + branch}, 20*time.Second)
	if r.ExitCode != 0 || r.Truncated {
		return "", fmt.Errorf("remote did not respond: %s", firstLine(r.Stderr+" "+r.Terminated))
	}
	fields := strings.Fields(r.Stdout)
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 || fields[1] != "refs/heads/"+branch {
		return "", fmt.Errorf("unexpected remote branch response")
	}
	return fields[0], nil
}

func (s Service) Run(ctx context.Context, repo *model.Repository, remoteCheck, lfsCheck, resourceCheck, policyCheck bool, aiFiles []string) model.PreflightResult {
	result := model.PreflightResult{Passed: true}
	add := func(name string, status model.ResultStatus, detail string, blocking bool) {
		result.Items = append(result.Items, model.PreflightItem{Name: name, Status: status, Detail: detail, Blocking: blocking})
		if blocking {
			result.Passed = false
		}
	}
	if repo.Root == "" {
		add("repository", model.StatusBlocked, "repository root unavailable", true)
		return result
	}
	add("repository", model.StatusPass, repo.Root, false)
	if repo.HEAD == "" {
		add("commits", model.StatusBlocked, "create a commit before pushing", true)
	}
	if repo.Target.DetachedHEAD {
		add("branch", model.StatusBlocked, "detached HEAD has no approved destination", true)
	} else if repo.MergeState || repo.RebaseState || repo.CherryPick || len(repo.Changes.Conflicts) > 0 {
		add("branch state", model.StatusBlocked, "finish the merge, rebase, cherry-pick, revert, or conflict resolution", true)
	} else {
		add("branch", model.StatusPass, repo.Branch+" -> "+repo.Target.Branch, false)
	}
	valid := safeGitArgument(repo.Target.Remote) && safeGitArgument(repo.Target.Branch) && repo.Target.RemoteURL != ""
	if valid {
		r := s.Runner.Run(ctx, repo.Root, []string{"git", "check-ref-format", "refs/heads/" + repo.Target.Branch}, 10*time.Second)
		valid = r.ExitCode == 0
	}
	if !valid {
		add("remote", model.StatusBlocked, "configure a valid push remote and destination branch", true)
	} else {
		add("remote", model.StatusPass, repo.Target.Remote+" -> "+repo.Target.RemoteURL, false)
	}
	if remoteCheck && valid {
		remoteHead, err := s.RemoteHEAD(ctx, repo.Root, repo.Target.RemoteURL, repo.Target.Branch)
		if err != nil {
			add("remote reachability", model.StatusBlocked, err.Error(), true)
		} else {
			result.RemoteHEAD = remoteHead
			add("remote reachability", model.StatusPass, "destination branch observed", false)
			if remoteHead != "" {
				available := s.Runner.Run(ctx, repo.Root, []string{"git", "cat-file", "-e", remoteHead + "^{commit}"}, 10*time.Second)
				if available.ExitCode != 0 {
					add("remote history", model.StatusBlocked, "remote advanced beyond local history; run git fetch and reconcile before retrying", true)
				} else {
					repo.Target.Base = remoteHead
					if err = (repository.Service{Runner: s.Runner}).Outgoing(ctx, repo); err != nil {
						add("outgoing commits", model.StatusBlocked, err.Error(), true)
					}
					ff := s.Runner.Run(ctx, repo.Root, []string{"git", "merge-base", "--is-ancestor", remoteHead, repo.HEAD}, 10*time.Second)
					if ff.ExitCode != 0 {
						add("fast-forward", model.StatusBlocked, "destination is not an ancestor of HEAD; reconcile history without a force push", true)
					} else {
						add("fast-forward", model.StatusPass, "no non-fast-forward risk observed", false)
					}
				}
			} else {
				repo.Target.Base = ""
				if err = (repository.Service{Runner: s.Runner}).Outgoing(ctx, repo); err != nil {
					add("outgoing commits", model.StatusBlocked, err.Error(), true)
				}
				add("fast-forward", model.StatusPass, "initial push to this branch", false)
			}
		}
	} else {
		add("remote reachability", model.StatusSkipped, "remote probe disabled", false)
	}
	add("authentication", model.StatusUnknown, "write credentials and server permissions are confirmed only by the actual push", false)
	if repo.Target.Upstream == "" {
		add("upstream", model.StatusWarning, "no configured upstream; the displayed explicit destination will be used", false)
	} else {
		add("upstream", model.StatusPass, repo.Target.Upstream, false)
	}
	if repo.HEAD != "" {
		s.largeObjects(ctx, repo, &result, add)
		add("outgoing commits", model.StatusPass, fmt.Sprintf("%d commit(s), %d file(s)", repo.Changes.Commits, len(repo.Changes.Files)), false)
	}
	if lfsCheck {
		if repo.LFSUsed && !repo.LFSInstalled {
			add("Git LFS", model.StatusBlocked, "LFS attributes found but git-lfs is unavailable", true)
		} else if repo.LFSUsed {
			// fsck checks local pointers and required objects without contacting billing APIs.
			args := []string{"git", "lfs", "fsck"}
			if repo.Target.Base != "" {
				args = append(args, repo.Target.Base+".."+repo.HEAD)
			} else if repo.HEAD != "" {
				args = append(args, repo.HEAD)
			}
			r := s.Runner.Run(ctx, repo.Root, args, 30*time.Second)
			if r.ExitCode != 0 {
				add("LFS objects", model.StatusBlocked, firstLine(r.Stderr+" "+r.Stdout+" "+r.Terminated), true)
			} else {
				add("LFS objects", model.StatusPass, "local pointers and objects verified", false)
			}
			r = s.Runner.Run(ctx, repo.Root, []string{"git", "lfs", "push", "--dry-run", repo.Target.Remote, "HEAD"}, 30*time.Second)
			if r.ExitCode != 0 {
				add("LFS upload plan", model.StatusBlocked, firstLine(r.Stderr+" "+r.Stdout), true)
			} else {
				add("LFS upload plan", model.StatusPass, "dry-run completed; no LFS objects uploaded", false)
			}
			add("LFS quota", model.StatusUnknown, "remote storage and bandwidth quota unavailable locally", false)
		} else {
			add("Git LFS", model.StatusSkipped, "no LFS attributes found", false)
		}
	}
	if resourceCheck {
		if len(repo.Project.CI) == 0 {
			add("CI resources", model.StatusSkipped, "no CI configuration detected", false)
		} else {
			add("CI configuration", model.StatusPass, strings.Join(repo.Project.CI, ", "), false)
			add("CI resources", model.StatusUnknown, "hosted runners, secrets, service capacity, and quota cannot be reproduced locally", false)
		}
		if f, err := os.CreateTemp(repo.GitDir, "pushguard-storage-*"); err != nil {
			add("local storage", model.StatusBlocked, "Git directory is not writable: "+err.Error(), true)
		} else {
			path := f.Name()
			_, writeErr := f.Write(make([]byte, 4096))
			if writeErr == nil {
				writeErr = f.Sync()
			}
			f.Close()
			os.Remove(path)
			if writeErr != nil {
				add("local storage", model.StatusBlocked, "Git directory cannot write temporary state: "+writeErr.Error(), true)
			} else {
				add("local storage", model.StatusPass, "Git directory accepts temporary state", false)
			}
		}
	}
	if len(repo.Changes.All) > 0 {
		add("working tree", model.StatusBlocked, "local edits must be committed and reverified; Git pushes committed content only", true)
	}
	if policyCheck {
		add("repository policy", model.StatusUnknown, "remote branch protection and hooks remain authoritative", false)
	}
	_ = aiFiles // provenance is reviewed by the workflow before any commit blocker.
	return result
}
func (s Service) largeObjects(ctx context.Context, repo *model.Repository, result *model.PreflightResult, add func(string, model.ResultStatus, string, bool)) {
	args := []string{"git", "rev-list", "--objects", repo.HEAD}
	if repo.Target.Base != "" {
		args = append(args, "^"+repo.Target.Base)
	}
	res := s.Runner.Run(ctx, repo.Root, args, 30*time.Second)
	if res.ExitCode != 0 || res.Truncated {
		add("outgoing objects", model.StatusBlocked, "cannot enumerate outgoing objects", true)
		return
	}
	var ids []string
	paths := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		ids = append(ids, parts[0])
		if len(parts) == 2 {
			paths[parts[0]] = parts[1]
		}
	}
	if len(ids) == 0 {
		add("outgoing objects", model.StatusPass, "no outgoing objects", false)
		return
	}
	sizes := s.Runner.RunInput(ctx, repo.Root, []string{"git", "cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)"}, 30*time.Second, strings.Join(ids, "\n")+"\n")
	if sizes.ExitCode != 0 || sizes.Truncated {
		add("outgoing objects", model.StatusBlocked, "cannot inspect outgoing object sizes", true)
		return
	}
	largest := int64(0)
	largePath := ""
	lines := strings.Split(strings.TrimSpace(sizes.Stdout), "\n")
	if len(lines) != len(ids) {
		add("outgoing objects", model.StatusBlocked, "incomplete outgoing object evidence", true)
		return
	}
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) != 3 {
			add("outgoing objects", model.StatusBlocked, "missing or malformed object evidence", true)
			return
		}
		size, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			add("outgoing objects", model.StatusBlocked, "invalid object size", true)
			return
		}
		result.Bytes += size
		if f[1] == "blob" && size > largest {
			largest = size
			largePath = paths[f[0]]
		}
	}
	if largest > 50*1024*1024 {
		add("outgoing objects", model.StatusBlocked, fmt.Sprintf("%s: %d bytes exceeds the 50 MiB policy; review history or use Git LFS", largePath, largest), true)
	} else {
		add("outgoing objects", model.StatusPass, "all outgoing blobs are within the 50 MiB policy", false)
	}
	add("push size", model.StatusPass, fmt.Sprintf("%d bytes before Git compression (estimate)", result.Bytes), false)
}
func safeGitArgument(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, " \t\n\r\x00") && !strings.Contains(s, "..") && !strings.Contains(s, "@{")
}
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// Writable is used by doctor without modifying repository contents.
func Writable(root string) error {
	f, err := os.CreateTemp(filepath.Clean(root), "pushguard-doctor-*")
	if err != nil {
		return err
	}
	name := f.Name()
	err = f.Close()
	os.Remove(name)
	return err
}

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/runner"
)

type Service struct{ Runner runner.Runner }

// FeatureTarget selects the current branch for PR delivery without modifying
// tracking configuration or accidentally pushing to an upstream base branch.
func (s Service) FeatureTarget(ctx context.Context, r *model.Repository) error {
	if r.Target.DetachedHEAD {
		return fmt.Errorf("PR delivery requires a feature branch")
	}
	r.Target.Branch = r.Branch
	r.Target.Refspec = "HEAD:refs/heads/" + r.Branch
	r.Target.Base = ""
	if r.Target.Remote != "" {
		if tip, err := s.Git(ctx, r.Root, "rev-parse", "--verify", "refs/remotes/"+r.Target.Remote+"/"+r.Branch); err == nil {
			r.Target.Base = strings.TrimSpace(tip)
		}
	}
	return s.Outgoing(ctx, r)
}

func (s Service) Git(ctx context.Context, root string, args ...string) (string, error) {
	res := s.Runner.Run(ctx, root, append([]string{"git"}, args...), 30*time.Second)
	if res.ExitCode != 0 || res.Truncated {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(res.Stderr+" "+res.Terminated))
	}
	return res.Stdout, nil
}

func (s Service) Discover(ctx context.Context, cwd string) (*model.Repository, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	output, err := s.Git(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("Git discovery failed: %w", err)
	}
	root := strings.TrimSpace(output)
	if root == "" {
		return nil, fmt.Errorf("Git returned an empty repository root")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	r := &model.Repository{Root: root, CWD: abs}
	output, err = s.Git(ctx, root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	r.GitDir = strings.TrimSpace(output)
	branch := s.Runner.Run(ctx, root, []string{"git", "symbolic-ref", "--short", "-q", "HEAD"}, 10*time.Second)
	if branch.ExitCode != 0 && branch.ExitCode != 1 {
		return nil, fmt.Errorf("cannot inspect branch: %s", branch.Stderr)
	}
	r.Branch = strings.TrimSpace(branch.Stdout)
	if r.Branch == "" {
		r.Target.DetachedHEAD = true
		r.Branch = "(detached HEAD)"
	}
	if output, e := s.Git(ctx, root, "rev-parse", "--verify", "HEAD"); e == nil {
		r.HEAD = strings.TrimSpace(output)
	}
	cfg := func(key string) string {
		res := s.Runner.Run(ctx, root, []string{"git", "config", "--get", key}, 10*time.Second)
		if res.ExitCode == 0 {
			return strings.TrimSpace(res.Stdout)
		}
		return ""
	}
	upstreamRemote := cfg("branch." + r.Branch + ".remote")
	merge := strings.TrimPrefix(cfg("branch."+r.Branch+".merge"), "refs/heads/")
	remote := cfg("branch." + r.Branch + ".pushRemote")
	if remote == "" {
		remote = cfg("remote.pushDefault")
	}
	if remote == "" && upstreamRemote != "." {
		remote = upstreamRemote
	}
	names, err := s.Git(ctx, root, "remote")
	if err != nil {
		return nil, err
	}
	if remote == "" {
		list := strings.Fields(names)
		for _, name := range list {
			if name == "origin" {
				remote = name
			}
		}
		if remote == "" && len(list) == 1 {
			remote = list[0]
		}
	}
	r.Target.Remote = remote
	r.Target.Branch = r.Branch
	if merge != "" && upstreamRemote == remote {
		r.Target.Branch = merge
	}
	if remote != "" {
		url, e := s.Git(ctx, root, "remote", "get-url", "--push", remote)
		if e == nil {
			r.Target.RemoteURL = strings.TrimSpace(url)
		}
		tracking := "refs/remotes/" + remote + "/" + r.Target.Branch
		if output, e := s.Git(ctx, root, "rev-parse", "--verify", tracking); e == nil {
			r.Target.Base = strings.TrimSpace(output)
		}
	}
	if output, e := s.Git(ctx, root, "rev-parse", "--symbolic-full-name", "@{upstream}"); e == nil {
		r.Target.Upstream = strings.TrimSpace(output)
	}
	r.Target.Refspec = "HEAD:refs/heads/" + r.Target.Branch
	r.Changes, err = s.changes(ctx, root)
	if err != nil {
		return nil, err
	}
	if err = s.Outgoing(ctx, r); err != nil {
		return nil, err
	}
	r.Project = detectProject(root)
	r.LFSInstalled = s.Runner.Run(ctx, root, []string{"git", "lfs", "version"}, 10*time.Second).ExitCode == 0
	// Attributes can live in nested directories; inspect tracked attribute files.
	attrs, err := s.Git(ctx, root, "ls-files", "-z", "--", ".gitattributes", "**/.gitattributes")
	if err != nil {
		return nil, err
	}
	for _, file := range strings.Split(attrs, "\x00") {
		if file == "" {
			continue
		}
		data, e := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if e == nil && strings.Contains(string(data), "filter=lfs") {
			r.LFSUsed = true
		}
	}
	if data, e := os.ReadFile(filepath.Join(root, ".gitattributes")); e == nil && strings.Contains(string(data), "filter=lfs") {
		r.LFSUsed = true
	}
	r.HasLFS = r.LFSInstalled || r.LFSUsed
	r.MergeState = fileExists(filepath.Join(r.GitDir, "MERGE_HEAD"))
	r.RebaseState = fileExists(filepath.Join(r.GitDir, "rebase-merge")) || fileExists(filepath.Join(r.GitDir, "rebase-apply"))
	r.CherryPick = fileExists(filepath.Join(r.GitDir, "CHERRY_PICK_HEAD")) || fileExists(filepath.Join(r.GitDir, "REVERT_HEAD"))
	return r, nil
}

// Outgoing covers the initial push as well as an existing upstream. The remote
// preflight may replace Base with the server's observed branch tip before calling it.
func (s Service) Outgoing(ctx context.Context, r *model.Repository) error {
	if r.HEAD == "" {
		return nil
	}
	args := []string{"rev-list", r.HEAD}
	if r.Target.Base != "" {
		args = append(args, "^"+r.Target.Base)
	}
	output, err := s.Git(ctx, r.Root, args...)
	if err != nil {
		return err
	}
	r.Changes.Outgoing = strings.Fields(output)
	r.Changes.Commits = len(r.Changes.Outgoing)
	r.Changes.Ahead = r.Changes.Commits
	if r.Target.Base != "" {
		output, err = s.Git(ctx, r.Root, "rev-list", r.Target.Base, "^"+r.HEAD)
		if err != nil {
			return err
		}
		r.Changes.Behind = len(strings.Fields(output))
		output, err = s.Git(ctx, r.Root, "diff", "--name-only", "-z", r.Target.Base, r.HEAD, "--")
	} else {
		output, err = s.Git(ctx, r.Root, "ls-tree", "-r", "--name-only", "-z", r.HEAD)
	}
	if err != nil {
		return err
	}
	r.Changes.Files = nonempty(strings.Split(output, "\x00"))
	return nil
}

func (s Service) changes(ctx context.Context, root string) (model.ChangeSet, error) {
	output, err := s.Git(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return model.ChangeSet{}, err
	}
	parts := strings.Split(output, "\x00")
	var cs model.ChangeSet
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if part == "" {
			continue
		}
		if len(part) < 4 {
			return cs, fmt.Errorf("malformed Git status")
		}
		xy, path := part[:2], part[3:]
		cs.All = append(cs.All, path)
		if strings.ContainsAny(xy, "RC") {
			i++
		} // NUL rename/copy source is a second record.
		if xy == "??" {
			cs.Untracked = append(cs.Untracked, path)
			continue
		}
		if xy == "UU" || xy == "AA" || xy == "DD" || strings.Contains(xy, "U") {
			cs.Conflicts = append(cs.Conflicts, path)
		}
		if xy[0] != ' ' {
			cs.Staged = append(cs.Staged, path)
		}
		if xy[1] != ' ' {
			cs.Unstaged = append(cs.Unstaged, path)
		}
	}
	sort.Strings(cs.All)
	sort.Strings(cs.Staged)
	sort.Strings(cs.Unstaged)
	sort.Strings(cs.Untracked)
	return cs, nil
}
func nonempty(values []string) []string {
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func detectProject(root string) model.Project {
	p := model.Project{Root: root, Name: filepath.Base(root)}
	indicators := []struct {
		name     string
		files    []string
		language string
	}{{"Node.js", []string{"package.json"}, "JavaScript/TypeScript"}, {"Go", []string{"go.mod"}, "Go"}, {"Python", []string{"pyproject.toml", "requirements.txt", "setup.py"}, "Python"}, {"Rust", []string{"Cargo.toml"}, "Rust"}, {"Java/Maven", []string{"pom.xml"}, "Java"}, {"Java/Gradle", []string{"build.gradle", "build.gradle.kts"}, "Java"}, {".NET", []string{"*.csproj"}, "C#"}, {"C/C++", []string{"CMakeLists.txt", "Makefile"}, "C/C++"}}
	for _, ind := range indicators {
		found := false
		for _, file := range ind.files {
			if strings.Contains(file, "*") {
				matches, _ := filepath.Glob(filepath.Join(root, file))
				found = len(matches) > 0
			} else {
				_, err := os.Stat(filepath.Join(root, file))
				found = err == nil
			}
			if found {
				break
			}
		}
		if found {
			p.Indicators = append(p.Indicators, ind.name)
			if !contains(p.Languages, ind.language) {
				p.Languages = append(p.Languages, ind.language)
			}
		}
	}
	if contains(p.Indicators, "Node.js") {
		manifest := readNodeManifest(root)
		if manifest.Name != "" {
			p.Name = manifest.Name
		}
		p.Scripts = sortedKeys(manifest.Scripts)
		p.Frameworks = nodeFrameworks(manifest)
		p.Workspace, p.WorkspacePackages = nodeWorkspace(manifest)
		p.PackageManager = nodePackageManager(root, manifest.PackageManager)
		if fileExists(filepath.Join(root, "turbo.json")) {
			p.Workspace = true
			p.Frameworks = appendUnique(p.Frameworks, "Turborepo")
		}
		if fileExists(filepath.Join(root, "nx.json")) {
			p.Workspace = true
			p.Frameworks = appendUnique(p.Frameworks, "Nx")
		}
	}
	if fileExists(filepath.Join(root, "go.work")) {
		p.Workspace = true
		p.WorkspacePackages = append(p.WorkspacePackages, "go.work")
	}
	ci := []string{".github/workflows", ".gitlab-ci.yml", "Jenkinsfile", "azure-pipelines.yml", ".circleci/config.yml"}
	for _, f := range ci {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			p.CI = append(p.CI, f)
		}
	}
	p.CILocalSteps, p.CIRemoteSteps = discoverCISteps(root, p.CI)
	return p
}

type nodeManifest struct {
	Name            string            `json:"name"`
	PackageManager  string            `json:"packageManager"`
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Workspaces      json.RawMessage   `json:"workspaces"`
}

func readNodeManifest(root string) nodeManifest {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nodeManifest{}
	}
	var manifest nodeManifest
	if json.Unmarshal(data, &manifest) != nil {
		return nodeManifest{}
	}
	return manifest
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func nodePackageManager(root, declared string) string {
	if declared != "" {
		if at := strings.IndexByte(declared, '@'); at > 0 {
			declared = declared[:at]
		}
		switch declared {
		case "npm", "pnpm", "yarn", "bun":
			return declared
		}
	}
	switch {
	case fileExists(filepath.Join(root, "pnpm-lock.yaml")):
		return "pnpm"
	case fileExists(filepath.Join(root, "yarn.lock")):
		return "yarn"
	case fileExists(filepath.Join(root, "bun.lockb")), fileExists(filepath.Join(root, "bun.lock")):
		return "bun"
	default:
		return "npm"
	}
}

func nodeFrameworks(manifest nodeManifest) []string {
	all := map[string]string{}
	for name, version := range manifest.Dependencies {
		all[name] = version
	}
	for name, version := range manifest.DevDependencies {
		all[name] = version
	}
	known := []struct{ packageName, label string }{
		{"typescript", "TypeScript"}, {"react", "React"}, {"next", "Next.js"}, {"vue", "Vue"},
		{"@angular/core", "Angular"}, {"express", "Express"}, {"fastify", "Fastify"}, {"vite", "Vite"},
		{"eslint", "ESLint"}, {"jest", "Jest"}, {"vitest", "Vitest"},
	}
	var out []string
	for _, item := range known {
		if _, ok := all[item.packageName]; ok {
			out = append(out, item.label)
		}
	}
	return out
}

func nodeWorkspace(manifest nodeManifest) (bool, []string) {
	if len(manifest.Workspaces) == 0 || string(manifest.Workspaces) == "null" {
		return false, nil
	}
	var packages []string
	if json.Unmarshal(manifest.Workspaces, &packages) == nil {
		return true, packages
	}
	var object struct {
		Packages []string `json:"packages"`
	}
	if json.Unmarshal(manifest.Workspaces, &object) == nil {
		return true, object.Packages
	}
	return true, nil
}

func appendUnique(values []string, value string) []string {
	if contains(values, value) {
		return values
	}
	return append(values, value)
}

func discoverCISteps(root string, ci []string) ([]string, []string) {
	var local, remote []string
	seenLocal, seenRemote := map[string]bool{}, map[string]bool{}
	add := func(target *[]string, seen map[string]bool, value string) {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			*target = append(*target, value)
		}
	}
	for _, entry := range ci {
		path := filepath.Join(root, entry)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.IsDir() {
			files, _ := os.ReadDir(path)
			for _, file := range files {
				if !file.IsDir() && (strings.HasSuffix(file.Name(), ".yml") || strings.HasSuffix(file.Name(), ".yaml")) {
					readCISteps(filepath.Join(path, file.Name()), add, &local, &remote, seenLocal, seenRemote)
				}
			}
			continue
		}
		readCISteps(path, add, &local, &remote, seenLocal, seenRemote)
	}
	return local, remote
}

func readCISteps(path string, add func(*[]string, map[string]bool, string), local, remote *[]string, seenLocal, seenRemote map[string]bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 1<<20 {
		return
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		line = strings.TrimPrefix(line, "- ")
		if !strings.HasPrefix(line, "run:") {
			continue
		}
		command := strings.TrimSpace(strings.TrimPrefix(line, "run:"))
		command = strings.Trim(command, "\"'")
		if !ciCommand(command) {
			continue
		}
		lower := strings.ToLower(command)
		if strings.Contains(lower, "deploy") || strings.Contains(lower, "publish") || strings.Contains(lower, "release") || strings.Contains(lower, "production") || strings.Contains(lower, "terraform apply") || strings.Contains(lower, "kubectl apply") || strings.Contains(lower, "docker push") {
			add(remote, seenRemote, command)
		} else {
			add(local, seenLocal, command)
		}
	}
}

func ciCommand(command string) bool {
	for _, token := range []string{"npm ", "pnpm ", "yarn ", "bun ", "go test", "go vet", "pytest", "ruff", "cargo ", "dotnet ", "mvn ", "gradle ", "make "} {
		if strings.Contains(command, token) {
			return true
		}
	}
	return false
}
func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// OutgoingDiff compares the complete initial tree against Git's empty tree.
// Showing just HEAD's last commit would omit earlier initial-push changes.
func (s Service) OutgoingDiff(ctx context.Context, r *model.Repository) (string, error) {
	if r.HEAD == "" {
		return "", nil
	}
	base := r.Target.Base
	if base == "" {
		result := s.Runner.RunInput(ctx, r.Root, []string{"git", "hash-object", "-t", "tree", "--stdin"}, 10*time.Second, "")
		if result.ExitCode != 0 {
			return "", fmt.Errorf("cannot compute empty tree")
		}
		base = strings.TrimSpace(result.Stdout)
	}
	return s.Git(ctx, r.Root, "diff", "--no-color", "--no-ext-diff", "--no-textconv", base, r.HEAD, "--")
}

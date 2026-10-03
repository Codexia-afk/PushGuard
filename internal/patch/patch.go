package patch

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/runner"
	"github.com/pushguard/pushguard/internal/security"
)

const (
	defaultMaxPatchBytes = 1 << 20
	defaultMaxPatchFiles = 20
	maxManifestBytes     = 8 << 20
	maxSnapshotFiles     = 10000
)

type Validator struct {
	MaxBytes  int
	MaxFiles  int
	Sensitive func(string) bool
}

// Validate checks both the syntax and the file policy of a proposal. A
// proposal's Files field is optional, but when present it must name exactly
// the files represented by the diff.
func (v Validator) Validate(root string, p model.RepairProposal) ([]string, error) {
	if v.MaxBytes <= 0 {
		v.MaxBytes = defaultMaxPatchBytes
	}
	if v.MaxFiles <= 0 {
		v.MaxFiles = defaultMaxPatchFiles
	}
	if len(p.Patch) == 0 {
		return nil, fmt.Errorf("empty patch")
	}
	if len(p.Patch) > v.MaxBytes {
		return nil, fmt.Errorf("patch exceeds %d bytes", v.MaxBytes)
	}
	changed := 0
	for _, line := range strings.Split(p.Patch, "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			changed++
		}
		if strings.HasPrefix(line, "+") {
			for _, suppression := range []string{"eslint-disable", "@ts-ignore", "@ts-nocheck", "istanbul ignore", "pragma: no cover", "test.skip(", "test.only(", "describe.skip(", "describe.only(", "# noqa", "# type: ignore"} {
				if strings.Contains(line, suppression) {
					return nil, fmt.Errorf("patch adds a verification suppression: %s", suppression)
				}
			}
		}
	}
	if changed > 2000 {
		return nil, fmt.Errorf("patch exceeds 2000 changed lines")
	}

	files, err := parsePatch(p.Patch)
	if err != nil {
		return nil, err
	}
	if len(files) > v.MaxFiles {
		return nil, fmt.Errorf("patch touches %d files; limit is %d", len(files), v.MaxFiles)
	}
	sensitive := v.Sensitive
	if sensitive == nil {
		sensitive = defaultSensitivePath
	}
	for _, f := range files {
		if err := validatePath(root, f, sensitive); err != nil {
			return nil, err
		}
	}
	if len(p.Files) != 0 {
		proposalFiles := make([]string, 0, len(p.Files))
		seen := make(map[string]bool, len(p.Files))
		for _, f := range p.Files {
			clean, err := normalizeRelativePath(f)
			if err != nil {
				return nil, fmt.Errorf("invalid proposal file %q: %w", f, err)
			}
			if seen[clean] {
				return nil, fmt.Errorf("proposal lists file more than once: %s", f)
			}
			seen[clean] = true
			if err := validatePath(root, clean, sensitive); err != nil {
				return nil, err
			}
			proposalFiles = append(proposalFiles, clean)
		}
		if !sameFileSet(files, proposalFiles) {
			return nil, fmt.Errorf("proposal Files does not match files in patch")
		}
	}
	return files, nil
}

func sameFileSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, f := range a {
		seen[f] = true
	}
	for _, f := range b {
		if !seen[f] {
			return false
		}
	}
	return true
}

// validatePath rejects paths that are unsafe even before git sees the patch.
// In particular, checking every existing parent prevents a symlinked parent
// from redirecting a safe-looking relative path outside the repository.
func validatePath(root, path string, sensitive func(string) bool) error {
	clean, err := normalizeRelativePath(path)
	if err != nil {
		return err
	}
	if disallowedPath(clean) || verificationEntrypoint(root, clean) {
		return fmt.Errorf("patch targets a protected file: %s", path)
	}
	if sensitive != nil && sensitive(clean) {
		return fmt.Errorf("patch targets sensitive file: %s", path)
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	rootInfo, err := os.Lstat(rootAbs)
	if err != nil {
		return fmt.Errorf("repository root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("repository root is not a real directory")
	}

	cur := rootAbs
	parts := strings.Split(filepath.FromSlash(clean), string(filepath.Separator))
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		info, statErr := os.Lstat(cur)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				// A missing component means all later components are also
				// missing. It is safe for git to create that path.
				break
			}
			return fmt.Errorf("inspect patch path %s: %w", path, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("patch refuses symlink path: %s", path)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("patch path has a non-directory parent: %s", path)
		}
		if i == len(parts)-1 {
			if !info.Mode().IsRegular() || info.Size() > 16<<20 {
				return fmt.Errorf("patch target must be a regular text file no larger than 16 MiB: %s", path)
			}
			data, err := os.ReadFile(cur)
			if err != nil {
				return err
			}
			if bytes.IndexByte(data, 0) >= 0 {
				return fmt.Errorf("binary patch target refused: %s", path)
			}
		}
	}
	return nil
}

// A check's launcher is verification policy even when it lives outside tests/.
// Identify literal script entrypoints without running package scripts/config.
func verificationEntrypoint(root, file string) bool {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil || len(data) > 1<<20 {
		return false
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return false
	}
	for name, command := range manifest.Scripts {
		switch strings.Split(name, ":")[0] {
		case "lint", "typecheck", "type-check", "test", "build", "check", "security", "audit", "format":
		default:
			continue
		}
		args, err := config.ParseCommand(command)
		if err != nil {
			continue
		}
		for i, arg := range args {
			switch filepath.Base(arg) {
			case "node", "node.exe", "python", "python3", "sh", "bash", "tsx", "ts-node":
				for _, target := range args[i+1:] {
					if filepath.ToSlash(filepath.Clean(target)) == file {
						return true
					}
				}
			}
		}
	}
	return false
}

func normalizeRelativePath(path string) (string, error) {
	if path == "" || path == "/dev/null" {
		return "", fmt.Errorf("invalid patch path")
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", fmt.Errorf("patch path contains NUL")
	}
	// Backslashes and colons are rejected even on Unix. This keeps a patch
	// produced for another platform from becoming traversal or a drive/ADS
	// path when consumed on Windows.
	if strings.ContainsAny(path, `\\:`) {
		return "", fmt.Errorf("patch path uses an unsupported separator or volume: %s", path)
	}
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "~") {
		return "", fmt.Errorf("patch path must be relative: %s", path)
	}
	if len(path) >= 2 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' {
		return "", fmt.Errorf("patch path uses a volume: %s", path)
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("patch path contains traversal: %s", path)
		}
		if strings.ContainsAny(part, "\r\n\t") {
			return "", fmt.Errorf("patch path contains metadata or control characters: %s", path)
		}
	}
	return strings.Join(parts, "/"), nil
}

func defaultSensitivePath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == ".env" || strings.Contains(base, "secret") || strings.Contains(base, "credential") || strings.Contains(base, "token") || strings.Contains(base, "password") || strings.Contains(base, "private") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx")
}

func disallowedPath(path string) bool {
	lower := strings.ToLower(path)
	parts := strings.Split(lower, "/")
	for _, part := range parts {
		switch part {
		case ".git", ".gitconfig", ".env", "config", "configs", ".config", "security", "securities", "policy", "policies", "dependency", "dependencies", "deps", "vendor", "third_party", "node_modules", ".venv", "test", "tests", "testing", "testdata", "fixtures":
			return true
		}
	}
	base := parts[len(parts)-1]
	if strings.Contains(base, "policy") || strings.Contains(base, "security") || strings.Contains(base, "credential") || strings.Contains(base, "secret") {
		return true
	}
	if strings.HasPrefix(base, ".pushguard") || strings.HasPrefix(lower, ".github/") || strings.Contains(lower, "/hooks/") {
		return true
	}
	if strings.HasSuffix(base, "_test.go") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.HasPrefix(base, "config.") || strings.Contains(base, "dependency") || strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") {
		return true
	}
	switch base {
	case "go.mod", "go.sum", "cargo.toml", "cargo.lock", "package.json", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb", "pyproject.toml", "pytest.ini", "tox.ini", "ruff.toml", ".ruff.toml", "makefile", "cmakelists.txt", "pom.xml", "build.gradle", "build.gradle.kts", "composer.json", "composer.lock", "gemfile", "gemfile.lock", "requirements.txt", "pipfile", "pipfile.lock":
		return true
	}
	if strings.Contains(base, "eslint") || strings.HasPrefix(base, "tsconfig") || strings.Contains(base, ".config.") || strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") || strings.HasSuffix(base, ".csproj") {
		return true
	}
	if strings.HasPrefix(base, "requirements-") && strings.HasSuffix(base, ".txt") {
		return true
	}
	return false
}

// PatchFiles returns the files in a valid unified diff. Invalid or unsupported
// diffs return nil; callers that need an error should use Validator.Validate.
func PatchFiles(p string) []string {
	files, err := parsePatch(p)
	if err != nil {
		return nil
	}
	return files
}

var hunkHeader = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@(?: .*)?$`)

func parsePatch(p string) ([]string, error) {
	if !utf8.ValidString(p) || strings.ContainsRune(p, 0) {
		return nil, fmt.Errorf("patch must be UTF-8 text without NUL bytes")
	}
	// CR is accepted only as part of CRLF content lines (files that use CRLF).
	if strings.Contains(strings.ReplaceAll(p, "\r\n", "\n"), "\r") {
		return nil, fmt.Errorf("patch must use LF or CRLF line endings")
	}
	lines := strings.Split(p, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty patch")
	}

	var files []string
	seen := map[string]bool{}
	for i := 0; i < len(lines); {
		var gitOld, gitNew string
		if strings.HasPrefix(lines[i], "diff --git ") {
			var err error
			gitOld, gitNew, err = parseGitHeader(lines[i])
			if err != nil {
				return nil, err
			}
			i++
			for i < len(lines) && !strings.HasPrefix(lines[i], "--- ") {
				if strings.HasPrefix(lines[i], "index ") {
					if err := validateIndexLine(lines[i]); err != nil {
						return nil, err
					}
					i++
					continue
				}
				return nil, unsupportedDiffMetadata(lines[i])
			}
		}
		if i >= len(lines) || !strings.HasPrefix(lines[i], "--- ") {
			return nil, fmt.Errorf("invalid unified diff: missing --- header")
		}
		oldPath, err := parseHeaderPath(lines[i], "a/")
		if err != nil {
			return nil, err
		}
		i++
		if i >= len(lines) || !strings.HasPrefix(lines[i], "+++ ") {
			return nil, fmt.Errorf("invalid unified diff: missing +++ header")
		}
		newPath, err := parseHeaderPath(lines[i], "b/")
		if err != nil {
			return nil, err
		}
		i++
		if gitOld != "" && oldPath != "/dev/null" && gitOld != oldPath {
			return nil, fmt.Errorf("diff header path does not match --- header")
		}
		if gitNew != "" && newPath != "/dev/null" && gitNew != newPath {
			return nil, fmt.Errorf("diff header path does not match +++ header")
		}
		if oldPath == "/dev/null" && newPath == "/dev/null" {
			return nil, fmt.Errorf("invalid unified diff: both paths are /dev/null")
		}
		if oldPath != "/dev/null" && newPath != "/dev/null" && oldPath != newPath {
			return nil, fmt.Errorf("renames and copies are not supported")
		}

		hunks := 0
		for i < len(lines) && strings.HasPrefix(lines[i], "@@ ") {
			oldCount, newCount, err := parseHunkHeader(lines[i])
			if err != nil {
				return nil, err
			}
			i++
			for oldCount > 0 || newCount > 0 {
				if i >= len(lines) {
					return nil, fmt.Errorf("invalid unified diff: truncated hunk")
				}
				line := lines[i]
				if line == `\ No newline at end of file` {
					i++
					continue
				}
				if line == "" {
					return nil, fmt.Errorf("invalid unified diff: hunk line has no marker")
				}
				switch line[0] {
				case ' ':
					oldCount--
					newCount--
				case '-':
					oldCount--
				case '+':
					newCount--
				default:
					return nil, fmt.Errorf("invalid unified diff: invalid hunk line")
				}
				if oldCount < 0 || newCount < 0 {
					return nil, fmt.Errorf("invalid unified diff: hunk line counts do not match")
				}
				i++
			}
			for i < len(lines) && lines[i] == `\ No newline at end of file` {
				i++
			}
			hunks++
		}
		if hunks == 0 {
			return nil, fmt.Errorf("invalid unified diff: missing hunk")
		}
		if oldPath != "/dev/null" && !seen[oldPath] {
			files = append(files, oldPath)
			seen[oldPath] = true
		}
		if newPath != "/dev/null" && !seen[newPath] {
			files = append(files, newPath)
			seen[newPath] = true
		}
		if i < len(lines) && !strings.HasPrefix(lines[i], "diff --git ") && !strings.HasPrefix(lines[i], "--- ") {
			return nil, fmt.Errorf("unsupported or malformed unified diff metadata: %s", lines[i])
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("patch contains no files")
	}
	return files, nil
}

func parseGitHeader(line string) (string, string, error) {
	fields := strings.Fields(line)
	if len(fields) != 4 || fields[0] != "diff" || fields[1] != "--git" || !strings.HasPrefix(fields[2], "a/") || !strings.HasPrefix(fields[3], "b/") {
		return "", "", fmt.Errorf("invalid unified diff: malformed diff --git header")
	}
	oldPath, err := normalizeRelativePath(strings.TrimPrefix(fields[2], "a/"))
	if err != nil {
		return "", "", err
	}
	newPath, err := normalizeRelativePath(strings.TrimPrefix(fields[3], "b/"))
	if err != nil {
		return "", "", err
	}
	return oldPath, newPath, nil
}

func parseHeaderPath(line, prefix string) (string, error) {
	path := strings.TrimPrefix(line, strings.SplitN(line, " ", 2)[0]+" ")
	if path == line || strings.ContainsAny(path, "\t\r\n") {
		return "", fmt.Errorf("invalid unified diff: header contains unsupported path metadata")
	}
	if path == "/dev/null" {
		return path, nil
	}
	if !strings.HasPrefix(path, prefix) {
		return "", fmt.Errorf("invalid unified diff: path must use %s", prefix)
	}
	return normalizeRelativePath(strings.TrimPrefix(path, prefix))
}

func validateIndexLine(line string) error {
	fields := strings.Fields(line)
	if len(fields) < 2 || len(fields) > 3 || fields[0] != "index" {
		return fmt.Errorf("invalid unified diff: malformed index metadata")
	}
	parts := strings.Split(fields[1], "..")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || !isHex(parts[0]) || !isHex(parts[1]) {
		return fmt.Errorf("invalid unified diff: malformed index metadata")
	}
	if len(fields) == 3 && fields[2] != "100644" && fields[2] != "100755" {
		return fmt.Errorf("unsupported diff mode metadata")
	}
	return nil
}

func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func unsupportedDiffMetadata(line string) error {
	for _, prefix := range []string{
		"similarity index ", "dissimilarity index ", "rename from ", "rename to ",
		"copy from ", "copy to ", "new file mode ", "deleted file mode ",
		"old mode ", "new mode ", "GIT binary patch", "Binary files ",
		"Subproject commit ", "literal ", "delta ",
	} {
		if strings.HasPrefix(line, prefix) {
			return fmt.Errorf("unsupported diff metadata: %s", line)
		}
	}
	return fmt.Errorf("unsupported unified diff metadata: %s", line)
}

func parseHunkHeader(line string) (int, int, error) {
	matches := hunkHeader.FindStringSubmatch(line)
	if matches == nil {
		return 0, 0, fmt.Errorf("invalid unified diff: malformed hunk header")
	}
	oldCount, newCount := 1, 1
	if matches[2] != "" {
		if _, err := fmt.Sscanf(matches[2], "%d", &oldCount); err != nil {
			return 0, 0, fmt.Errorf("invalid unified diff: malformed old hunk count")
		}
	}
	if matches[4] != "" {
		if _, err := fmt.Sscanf(matches[4], "%d", &newCount); err != nil {
			return 0, 0, fmt.Errorf("invalid unified diff: malformed new hunk count")
		}
	}
	return oldCount, newCount, nil
}

// Check independently validates a proposal and asks git whether it applies.
// Apply performs the same validation itself; callers should not rely on a
// prior Check to make a later Apply safe.
func (v Validator) Check(ctx context.Context, root string, p model.RepairProposal, r runner.Runner) (model.CommandResult, error) {
	if _, err := v.Validate(root, p); err != nil {
		return model.CommandResult{}, err
	}
	return checkWithGit(ctx, root, p.Patch, r)
}

func Check(ctx context.Context, root string, p model.RepairProposal, r runner.Runner) (model.CommandResult, error) {
	return (Validator{}).Check(ctx, root, p, r)
}

func checkWithGit(ctx context.Context, root, patch string, r runner.Runner) (model.CommandResult, error) {
	name, cleanup, err := writeTempPatch(patch)
	if err != nil {
		return model.CommandResult{}, err
	}
	defer cleanup()
	if ctx == nil {
		ctx = context.Background()
	}
	result := r.Run(ctx, root, gitApply("--check", "--whitespace=nowarn", "--", name), 30*time.Second)
	if result.ExitCode != 0 {
		return result, fmt.Errorf("patch no longer applies: %s", strings.TrimSpace(result.Stderr))
	}
	return result, nil
}

func writeTempPatch(patch string) (string, func(), error) {
	tmp, err := os.CreateTemp("", "pushguard-patch-*.diff")
	if err != nil {
		return "", func() {}, err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		cleanup()
		return "", func() {}, err
	}
	if _, err := tmp.WriteString(patch); err != nil {
		tmp.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return name, cleanup, nil
}

type SnapshotStore struct{ Base string }

type fileState struct {
	Exists bool   `json:"exists"`
	Hash   string `json:"hash,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
}

type snapshotManifest struct {
	model.Snapshot
	Sealed   bool                 `json:"sealed,omitempty"`
	Expected map[string]fileState `json:"expected,omitempty"`
}

func (s SnapshotStore) Create(root string, files []string) (snap model.Snapshot, err error) {
	var total int64
	for _, file := range files {
		info, e := os.Stat(filepath.Join(root, filepath.FromSlash(file)))
		if e == nil {
			if info.Size() > 16<<20 {
				return snap, fmt.Errorf("snapshot file exceeds 16 MiB: %s", file)
			}
			total += info.Size()
			if total > 64<<20 {
				return snap, fmt.Errorf("snapshot exceeds 64 MiB total")
			}
		} else if !os.IsNotExist(e) {
			return snap, e
		}
	}
	canonical, err := canonicalRoot(root)
	if err != nil {
		return model.Snapshot{}, err
	}
	cleanFiles, err := cleanSnapshotFiles(canonical, files)
	if err != nil {
		return model.Snapshot{}, err
	}
	if len(cleanFiles) == 0 {
		return model.Snapshot{}, fmt.Errorf("cannot create an empty snapshot")
	}
	repo, err := s.repoDir(canonical, true)
	if err != nil {
		return model.Snapshot{}, err
	}
	snapshotsDir := filepath.Join(repo, "snapshots")
	if err := ensurePrivateDir(snapshotsDir); err != nil {
		return model.Snapshot{}, err
	}

	var dir string
	for attempt := 0; attempt < 16; attempt++ {
		id, idErr := newSnapshotID()
		if idErr != nil {
			return model.Snapshot{}, idErr
		}
		candidate := filepath.Join(snapshotsDir, id)
		if mkErr := os.Mkdir(candidate, 0700); mkErr == nil {
			dir, snap = candidate, model.Snapshot{ID: id, Root: canonical, CreatedAt: time.Now().UTC(), Files: cleanFiles, Modes: map[string]uint32{}, Dir: candidate}
			break
		} else if !os.IsExist(mkErr) {
			return model.Snapshot{}, mkErr
		}
	}
	if dir == "" {
		return model.Snapshot{}, fmt.Errorf("could not allocate a unique snapshot directory")
	}
	removeOnError := true
	defer func() {
		if removeOnError && dir != "" {
			_ = os.RemoveAll(dir)
		}
	}()

	filesDir := filepath.Join(dir, "files")
	if err := ensurePrivateDir(filesDir); err != nil {
		return snap, err
	}
	for _, f := range cleanFiles {
		full := filepath.Join(canonical, filepath.FromSlash(f))
		info, statErr := os.Lstat(full)
		entry := filepath.Join(filesDir, filepath.FromSlash(f))
		if statErr != nil {
			if !os.IsNotExist(statErr) {
				return snap, statErr
			}
			if err := ensurePrivateDir(filepath.Dir(entry)); err != nil {
				return snap, err
			}
			if err := writeExclusive(entry+".missing", nil, 0600); err != nil {
				return snap, err
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return snap, fmt.Errorf("cannot snapshot non-regular file %s", f)
		}
		snap.Modes[f] = uint32(info.Mode().Perm())
		data, readErr := os.ReadFile(full)
		if readErr != nil {
			return snap, readErr
		}
		if err := ensurePrivateDir(filepath.Dir(entry)); err != nil {
			return snap, err
		}
		// Snapshot contents are private; the original mode is retained in
		// the manifest and applied only when restoring the repository file.
		if err := writeExclusive(entry, data, 0600); err != nil {
			return snap, err
		}
	}
	manifest := snapshotManifest{Snapshot: snap}
	if err := writeManifest(dir, manifest); err != nil {
		return snap, err
	}
	if err := s.writeLatest(canonical, manifest); err != nil {
		return snap, err
	}
	removeOnError = false
	return snap, nil
}

func cleanSnapshotFiles(root string, files []string) ([]string, error) {
	if len(files) > maxSnapshotFiles {
		return nil, fmt.Errorf("snapshot contains too many files")
	}
	out := make([]string, 0, len(files))
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		clean, err := normalizeRelativePath(f)
		if err != nil {
			return nil, err
		}
		if disallowedPath(clean) {
			return nil, fmt.Errorf("snapshot targets a protected file: %s", f)
		}
		if seen[clean] {
			return nil, fmt.Errorf("snapshot lists file more than once: %s", f)
		}
		seen[clean] = true
		if err := validatePath(root, clean, nil); err != nil {
			return nil, err
		}
		out = append(out, clean)
	}
	return out, nil
}

func (s SnapshotStore) base() string {
	if s.Base != "" {
		return s.Base
	}
	if base := os.Getenv("PUSHGUARD_CACHE_DIR"); base != "" {
		return filepath.Join(base, "snapshots")
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "pushguard-snapshots")
	}
	return filepath.Join(base, "pushguard", "snapshots")
}

func (s SnapshotStore) repoDir(root string, create bool) (string, error) {
	base, err := s.ensureBase()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(root))
	repo := filepath.Join(base, "repos", hex.EncodeToString(sum[:]))
	if create {
		if err := ensurePrivateDir(filepath.Join(base, "repos")); err != nil {
			return "", err
		}
		if err := ensurePrivateDir(repo); err != nil {
			return "", err
		}
		if err := ensurePrivateDir(filepath.Join(repo, "snapshots")); err != nil {
			return "", err
		}
	} else if err := requirePrivateDir(repo); err != nil {
		return "", err
	}
	return repo, nil
}

func (s SnapshotStore) ensureBase() (string, error) {
	base, err := filepath.Abs(s.base())
	if err != nil {
		return "", err
	}
	if err := ensurePrivateDir(base); err != nil {
		return "", err
	}
	return base, nil
}

func (s SnapshotStore) writeLatest(root string, manifest snapshotManifest) error {
	repo, err := s.repoDir(root, true)
	if err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(repo, "latest.json"), manifest)
}

// Latest is safe when the store contains one repository. If more than one
// repository has a latest snapshot, it refuses to guess; use LatestFor.
func (s SnapshotStore) Latest() (model.Snapshot, error) {
	base, err := s.ensureBase()
	if err != nil {
		return model.Snapshot{}, err
	}
	repos := filepath.Join(base, "repos")
	entries, err := os.ReadDir(repos)
	if err != nil {
		return model.Snapshot{}, err
	}
	var found *snapshotManifest
	for _, entry := range entries {
		if !isHashDirName(entry.Name()) || !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(repos, entry.Name())
		if err := requirePrivateDir(candidate); err != nil {
			return model.Snapshot{}, err
		}
		latest := filepath.Join(candidate, "latest.json")
		info, statErr := os.Lstat(latest)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return model.Snapshot{}, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return model.Snapshot{}, fmt.Errorf("unsafe latest snapshot manifest")
		}
		m, readErr := readManifestFile(latest)
		if readErr != nil {
			return model.Snapshot{}, readErr
		}
		if found != nil {
			return model.Snapshot{}, fmt.Errorf("latest snapshot is ambiguous across repositories; use LatestFor")
		}
		found = &m
	}
	if found == nil {
		return model.Snapshot{}, os.ErrNotExist
	}
	loaded, err := s.loadManifest(found.Snapshot.Root, found.Snapshot)
	if err != nil {
		return model.Snapshot{}, err
	}
	return loaded.Snapshot, nil
}

// LatestFor selects the latest snapshot by its repository identity, avoiding
// the cross-repository ambiguity of Latest.
func (s SnapshotStore) LatestFor(root string) (model.Snapshot, error) {
	canonical, err := canonicalRoot(root)
	if err != nil {
		return model.Snapshot{}, err
	}
	repo, err := s.repoDir(canonical, false)
	if err != nil {
		return model.Snapshot{}, err
	}
	m, err := readManifestFile(filepath.Join(repo, "latest.json"))
	if err != nil {
		return model.Snapshot{}, err
	}
	loaded, err := s.loadManifest(canonical, m.Snapshot)
	if err != nil {
		return model.Snapshot{}, err
	}
	return loaded.Snapshot, nil
}

func isHashDirName(name string) bool {
	return len(name) == sha256.Size*2 && isHex(name)
}

func (s SnapshotStore) ClearLatest() error {
	base, err := s.ensureBase()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(base, "repos"))
	if err != nil {
		return err
	}
	var latest string
	for _, entry := range entries {
		if !isHashDirName(entry.Name()) || !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(base, "repos", entry.Name(), "latest.json")
		if _, statErr := os.Lstat(candidate); statErr == nil {
			if latest != "" {
				return fmt.Errorf("latest snapshot is ambiguous across repositories; use ClearLatestFor")
			}
			latest = candidate
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
	}
	if latest == "" {
		return os.ErrNotExist
	}
	return os.Remove(latest)
}

func (s SnapshotStore) ClearLatestFor(root string) error {
	canonical, err := canonicalRoot(root)
	if err != nil {
		return err
	}
	repo, err := s.repoDir(canonical, false)
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(repo, "latest.json"))
}

// Seal records the state after Apply. Restore refuses an unsealed snapshot
// and refuses to run if any affected path no longer matches this state.
func (s SnapshotStore) Seal(root string, snap model.Snapshot) error {
	canonical, err := canonicalRoot(root)
	if err != nil {
		return err
	}
	m, err := s.loadManifest(canonical, snap)
	if err != nil {
		return err
	}
	if m.Sealed {
		return fmt.Errorf("snapshot is already sealed")
	}
	expected := make(map[string]fileState, len(m.Snapshot.Files))
	for _, f := range m.Snapshot.Files {
		state, stateErr := currentState(canonical, f)
		if stateErr != nil {
			return stateErr
		}
		expected[f] = state
	}
	m.Expected = expected
	m.Sealed = true
	if err := writeManifest(m.Snapshot.Dir, m); err != nil {
		return err
	}
	// Do not move another repository's latest pointer backwards. Create puts
	// this snapshot in latest, but a concurrent Create may have superseded it.
	repo, err := s.repoDir(canonical, false)
	if err != nil {
		return err
	}
	latestPath := filepath.Join(repo, "latest.json")
	if latest, latestErr := readManifestFile(latestPath); latestErr == nil && latest.Snapshot.ID == m.Snapshot.ID {
		if err := writeJSONAtomic(latestPath, m); err != nil {
			return err
		}
	}
	return nil
}

func (s SnapshotStore) Restore(root string, snap model.Snapshot) error {
	canonical, err := canonicalRoot(root)
	if err != nil {
		return err
	}
	m, err := s.loadManifest(canonical, snap)
	if err != nil {
		return err
	}
	if !m.Sealed || len(m.Expected) != len(m.Snapshot.Files) {
		return fmt.Errorf("snapshot has no post-apply seal; refusing rollback")
	}
	for _, f := range m.Snapshot.Files {
		want, ok := m.Expected[f]
		if !ok {
			return fmt.Errorf("snapshot seal is incomplete")
		}
		got, stateErr := currentState(canonical, f)
		if stateErr != nil {
			return stateErr
		}
		if got != want {
			return fmt.Errorf("rollback refused: current state of %s differs from post-apply state", f)
		}
	}

	for _, f := range m.Snapshot.Files {
		if err := validatePath(canonical, f, nil); err != nil {
			return err
		}
		entry := filepath.Join(m.Snapshot.Dir, "files", filepath.FromSlash(f))
		if mode, ok := m.Snapshot.Modes[f]; ok {
			data, readErr := readPrivateFile(entry)
			if readErr != nil {
				return readErr
			}
			if err := restoreFile(canonical, f, data, os.FileMode(mode)); err != nil {
				return err
			}
		} else {
			if _, statErr := os.Lstat(entry + ".missing"); statErr != nil {
				return fmt.Errorf("snapshot original is missing: %s", f)
			}
			if err := removeFile(canonical, f); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s SnapshotStore) loadManifest(root string, snap model.Snapshot) (snapshotManifest, error) {
	canonical, err := canonicalRoot(root)
	if err != nil {
		return snapshotManifest{}, err
	}
	if snap.ID == "" || !validSnapshotID(snap.ID) {
		return snapshotManifest{}, fmt.Errorf("invalid snapshot id")
	}
	if snap.Root == "" {
		return snapshotManifest{}, fmt.Errorf("snapshot has no repository root")
	}
	storedRoot, err := canonicalRoot(snap.Root)
	if err != nil || storedRoot != canonical {
		return snapshotManifest{}, fmt.Errorf("snapshot belongs to a different repository")
	}
	repo, err := s.repoDir(canonical, false)
	if err != nil {
		return snapshotManifest{}, err
	}
	expectedDir := filepath.Join(repo, "snapshots", snap.ID)
	if filepath.Clean(snap.Dir) != filepath.Clean(expectedDir) {
		return snapshotManifest{}, fmt.Errorf("snapshot directory is not trusted")
	}
	if err := requirePrivateDir(expectedDir); err != nil {
		return snapshotManifest{}, err
	}
	m, err := readManifestFile(filepath.Join(expectedDir, "manifest.json"))
	if err != nil {
		return snapshotManifest{}, err
	}
	if err := validateManifest(canonical, expectedDir, m); err != nil {
		return snapshotManifest{}, err
	}
	if !sameSnapshot(snap, m.Snapshot) {
		return snapshotManifest{}, fmt.Errorf("snapshot manifest does not match supplied snapshot")
	}
	return m, nil
}

func sameSnapshot(a, b model.Snapshot) bool {
	if a.ID != b.ID || a.Root != b.Root || filepath.Clean(a.Dir) != filepath.Clean(b.Dir) || !sameOrderedFiles(a.Files, b.Files) {
		return false
	}
	if len(a.Modes) != len(b.Modes) {
		return false
	}
	for k, v := range a.Modes {
		if b.Modes[k] != v {
			return false
		}
	}
	return true
}

func sameOrderedFiles(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func validateManifest(root, dir string, m snapshotManifest) error {
	if !validSnapshotID(m.Snapshot.ID) || filepath.Clean(m.Snapshot.Dir) != filepath.Clean(dir) || m.Snapshot.Root != root {
		return fmt.Errorf("invalid snapshot manifest identity")
	}
	if len(m.Snapshot.Files) == 0 || len(m.Snapshot.Files) > maxSnapshotFiles {
		return fmt.Errorf("invalid snapshot file list")
	}
	seen := make(map[string]bool, len(m.Snapshot.Files))
	for _, f := range m.Snapshot.Files {
		clean, err := normalizeRelativePath(f)
		if err != nil || clean != f || seen[f] {
			return fmt.Errorf("invalid snapshot path")
		}
		seen[f] = true
		if err := validatePath(root, f, nil); err != nil {
			return err
		}
	}
	for f, mode := range m.Snapshot.Modes {
		if !seen[f] || mode > 0777 {
			return fmt.Errorf("invalid snapshot mode")
		}
	}
	if !m.Sealed && len(m.Expected) != 0 {
		return fmt.Errorf("unsealed snapshot contains expected state")
	}
	if m.Sealed && len(m.Expected) != len(m.Snapshot.Files) {
		return fmt.Errorf("sealed snapshot has incomplete expected state")
	}
	for f, state := range m.Expected {
		if !seen[f] || (state.Exists && (len(state.Hash) != sha256.Size*2 || !isHex(state.Hash) || state.Mode > 0777)) || (!state.Exists && (state.Hash != "" || state.Mode != 0)) {
			return fmt.Errorf("invalid expected state")
		}
	}
	return nil
}

func canonicalRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("repository root is not a real directory")
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func currentState(root, path string) (fileState, error) {
	if err := validatePath(root, path, nil); err != nil {
		return fileState{}, err
	}
	full := filepath.Join(root, filepath.FromSlash(path))
	info, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return fileState{}, nil
		}
		return fileState{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fileState{}, fmt.Errorf("snapshot path is not a regular file: %s", path)
	}
	file, err := os.Open(full)
	if err != nil {
		return fileState{}, err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return fileState{}, copyErr
	}
	if closeErr != nil {
		return fileState{}, closeErr
	}
	return fileState{Exists: true, Hash: hex.EncodeToString(hash.Sum(nil)), Mode: uint32(info.Mode().Perm())}, nil
}

func restoreFile(root, path string, data []byte, mode os.FileMode) error {
	clean, err := normalizeRelativePath(path)
	if err != nil || clean != path {
		return fmt.Errorf("invalid restore path")
	}
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := validatePath(root, path, nil); err != nil {
		return err
	}
	parent := filepath.Dir(full)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	if err := validatePath(root, path, nil); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(parent, ".pushguard-restore-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode.Perm()); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := validatePath(root, path, nil); err != nil {
		return err
	}
	return os.Rename(tmpName, full)
}

func removeFile(root, path string) error {
	if err := validatePath(root, path, nil); err != nil {
		return err
	}
	full := filepath.Join(root, filepath.FromSlash(path))
	if info, err := os.Lstat(full); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
			return fmt.Errorf("refusing to remove unsafe rollback target: %s", path)
		}
	} else if os.IsNotExist(err) {
		return nil
	} else {
		return err
	}
	return os.Remove(full)
}

func validSnapshotID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == 'T' || r == 'Z') {
			return false
		}
	}
	return !strings.Contains(id, "..")
}

func newSnapshotID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random[:]), nil
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("unsafe snapshot directory: %s", path)
	}
	return security.SecureDirectory(path)
}

func requirePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !security.PrivateMode(info.Mode()) {
		return fmt.Errorf("unsafe snapshot directory: %s", path)
	}
	return nil
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(mode.Perm()); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func writeManifest(dir string, m snapshotManifest) error {
	return writeJSONAtomic(filepath.Join(dir, "manifest.json"), m)
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	parent := filepath.Dir(path)
	if err := requirePrivateDir(parent); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(parent, ".pushguard-manifest-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func readManifestFile(path string) (snapshotManifest, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return snapshotManifest{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !security.PrivateMode(info.Mode()) || info.Size() > maxManifestBytes {
		return snapshotManifest{}, fmt.Errorf("unsafe snapshot manifest")
	}
	file, err := os.Open(path)
	if err != nil {
		return snapshotManifest{}, err
	}
	defer file.Close()
	var m snapshotManifest
	decoder := json.NewDecoder(io.LimitReader(file, maxManifestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return snapshotManifest{}, fmt.Errorf("read snapshot manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return snapshotManifest{}, fmt.Errorf("snapshot manifest has trailing data")
		}
		return snapshotManifest{}, fmt.Errorf("read snapshot manifest: %w", err)
	}
	return m, nil
}

func readPrivateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !security.PrivateMode(info.Mode()) {
		return nil, fmt.Errorf("unsafe snapshot file")
	}
	return os.ReadFile(path)
}

func Apply(ctx context.Context, root string, proposal model.RepairProposal, files []string, r runner.Runner) (model.CommandResult, error) {
	actual, err := (Validator{}).Validate(root, proposal)
	if err != nil {
		return model.CommandResult{}, err
	}
	modes := map[string]os.FileMode{}
	for _, file := range actual {
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(file))); err == nil {
			modes[file] = info.Mode().Perm()
		}
	}
	if len(files) != 0 {
		cleanFiles := make([]string, 0, len(files))
		seen := make(map[string]bool, len(files))
		for _, f := range files {
			clean, cleanErr := normalizeRelativePath(f)
			if cleanErr != nil || seen[clean] {
				return model.CommandResult{}, fmt.Errorf("files supplied to Apply do not match patch")
			}
			seen[clean] = true
			cleanFiles = append(cleanFiles, clean)
		}
		if !sameFileSet(actual, cleanFiles) {
			return model.CommandResult{}, fmt.Errorf("files supplied to Apply do not match patch")
		}
	}
	name, cleanup, err := writeTempPatch(proposal.Patch)
	if err != nil {
		return model.CommandResult{}, err
	}
	defer cleanup()
	if ctx == nil {
		ctx = context.Background()
	}
	check := r.Run(ctx, root, gitApply("--check", "--whitespace=nowarn", "--", name), 30*time.Second)
	if check.ExitCode != 0 {
		return check, fmt.Errorf("patch no longer applies: %s", strings.TrimSpace(check.Stderr))
	}
	applied := r.Run(ctx, root, gitApply("--whitespace=nowarn", "--", name), 30*time.Second)
	if applied.ExitCode != 0 {
		return applied, fmt.Errorf("apply patch: %s", strings.TrimSpace(applied.Stderr))
	}
	for file, mode := range modes {
		full := filepath.Join(root, filepath.FromSlash(file))
		if _, err := os.Lstat(full); os.IsNotExist(err) {
			continue
		}
		if err := validatePath(root, file, nil); err != nil {
			return applied, err
		}
		if err := os.Chmod(full, mode); err != nil {
			return applied, err
		}
	}
	return applied, nil
}

// gitApply builds a byte-exact git apply invocation. End-of-line conversion
// is disabled so the applied bytes equal the validated candidate content and
// a global core.autocrlf setting can never rewrite a file's line endings.
func gitApply(args ...string) []string {
	return append([]string{"git", "-c", "core.autocrlf=false", "-c", "core.eol=lf", "-c", "core.safecrlf=false", "apply"}, args...)
}

package patch

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/security"
)

// EditableFile applies the same repository and protected-path policy used for
// application before offering source locations to a proposal-only provider.
func EditableFile(root, file string) bool {
	return validatePath(root, file, security.SensitivePath) == nil
}

// Materialize only reads files and prepares a proposal. No source write occurs.
// Exact, unique anchors avoid fuzzy edits and let local models propose a repair
// without calculating unified-diff hunk counts. Validate/Check still run next.
func Materialize(root string, proposal *model.RepairProposal) error {
	if len(proposal.Edits) == 0 {
		return nil
	}
	if proposal.Patch != "" {
		return fmt.Errorf("proposal cannot mix a diff with replacement edits")
	}
	preview, err := PreviewEdits(root, proposal.Edits)
	if err != nil {
		return err
	}
	var files []string
	var diff strings.Builder
	for _, file := range preview {
		files = append(files, file.File)
		diff.WriteString(unified(file.File, file.Before, file.After))
		if diff.Len() > defaultMaxPatchBytes {
			return fmt.Errorf("generated diff exceeds patch limit")
		}
	}
	if len(proposal.Files) != 0 && !sameFileSet(proposal.Files, files) {
		return fmt.Errorf("proposal files do not match replacement edits")
	}
	proposal.Files, proposal.Patch = files, diff.String()
	return nil
}

type EditPreview struct{ File, Before, After string }

// PreviewEdits materializes exact replacements in memory for diff generation
// and optional language-parser checks. It never writes repository files.
func PreviewEdits(root string, edits []model.TextEdit) ([]EditPreview, error) {
	if len(edits) > 100 {
		return nil, fmt.Errorf("proposal exceeds 100 replacement edits")
	}
	before, after := map[string]string{}, map[string]string{}
	type replacement struct {
		start, end int
		text       string
	}
	replacements := map[string][]replacement{}
	var files []string
	for _, edit := range edits {
		if err := validatePath(root, edit.File, security.SensitivePath); err != nil {
			return nil, err
		}
		if _, seen := before[edit.File]; !seen {
			if len(files) >= defaultMaxPatchFiles {
				return nil, fmt.Errorf("too many edit files")
			}
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(edit.File)))
			if err != nil {
				return nil, err
			}
			before[edit.File], after[edit.File] = string(data), string(data)
			files = append(files, edit.File)
		}
		if edit.OldText == "" || strings.Count(before[edit.File], edit.OldText) != 1 {
			anchor := edit.OldText
			if len(anchor) > 200 {
				anchor = anchor[:200] + "..."
			}
			return nil, fmt.Errorf("oldText must match exactly once in %s; proposed anchor: %q", edit.File, security.Redact(anchor))
		}
		if len(edit.NewText) > defaultMaxPatchBytes {
			return nil, fmt.Errorf("replacement exceeds patch size limit")
		}
		if edit.OldText == edit.NewText {
			return nil, fmt.Errorf("replacement makes no change to %s", edit.File)
		}
		start := strings.Index(before[edit.File], edit.OldText)
		replacements[edit.File] = append(replacements[edit.File], replacement{start, start + len(edit.OldText), edit.NewText})
	}
	var preview []EditPreview
	for _, file := range files {
		edits := replacements[file]
		sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
		var content strings.Builder
		end := 0
		for _, edit := range edits {
			if edit.start < end {
				return nil, fmt.Errorf("overlapping replacement edits in %s; use one combined edit", file)
			}
			content.WriteString(before[file][end:edit.start])
			content.WriteString(edit.text)
			end = edit.end
		}
		content.WriteString(before[file][end:])
		after[file] = content.String()
		if before[file] == after[file] {
			return nil, fmt.Errorf("proposal makes no change to %s", file)
		}
		preview = append(preview, EditPreview{File: file, Before: before[file], After: after[file]})
	}
	return preview, nil
}

func sourceLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func unified(file, old, new string) string {
	a, b := sourceLines(old), sourceLines(new)
	equal := func(i, j int) bool {
		return a[i] == b[j] && (i == len(a)-1 && !strings.HasSuffix(old, "\n")) == (j == len(b)-1 && !strings.HasSuffix(new, "\n"))
	}
	prefix := 0
	for prefix < len(a) && prefix < len(b) && equal(prefix, prefix) {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && equal(len(a)-1-suffix, len(b)-1-suffix) {
		suffix++
	}
	start := max(0, prefix-3)
	oldEnd, newEnd := min(len(a), len(a)-suffix+3), min(len(b), len(b)-suffix+3)
	oldStart, newStart := start+1, start+1
	if oldEnd == start {
		oldStart = start
	}
	if newEnd == start {
		newStart = start
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n@@ -%d,%d +%d,%d @@\n", file, file, oldStart, oldEnd-start, newStart, newEnd-start)
	line := func(marker byte, lines []string, i int, source string) {
		out.WriteByte(marker)
		out.WriteString(lines[i])
		out.WriteByte('\n')
		if i == len(lines)-1 && !strings.HasSuffix(source, "\n") {
			out.WriteString("\\ No newline at end of file\n")
		}
	}
	for i := start; i < prefix; i++ {
		line(' ', a, i, old)
	}
	for i := prefix; i < len(a)-suffix; i++ {
		line('-', a, i, old)
	}
	for i := prefix; i < len(b)-suffix; i++ {
		line('+', b, i, new)
	}
	for i := len(a) - suffix; i < oldEnd; i++ {
		line(' ', a, i, old)
	}
	return out.String()
}

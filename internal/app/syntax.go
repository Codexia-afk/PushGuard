package app

import (
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/detector"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/patch"
	"github.com/pushguard/pushguard/internal/security"
)

// Parse candidate TypeScript using the project's installed compiler. Source is
// stdin data, never evaluated; neither project config nor source is executed.
const typescriptSyntaxScript = `
const fs = require('node:fs');
const ts = require(require.resolve('typescript', { paths: [process.cwd()] }));
const files = JSON.parse(fs.readFileSync(0, 'utf8'));
const path = require('node:path');
let failed = false;
for (const file of files) {
  const source = ts.createSourceFile(file.file, file.content, ts.ScriptTarget.Latest, true);
  for (const diagnostic of source.parseDiagnostics) {
    const loc = source.getLineAndCharacterOfPosition(diagnostic.start || 0);
    console.log(file.file + ':' + (loc.line + 1) + ':' + (loc.character + 1) + ': ' + ts.flattenDiagnosticMessageText(diagnostic.messageText, ' '));
    failed = true;
  }
}
// Overlay candidates in the compiler host, without writing or emitting files.
// Existing semantic failures are allowed; additional ones reject the candidate.
if (!failed) {
  const configPath = ts.findConfigFile(process.cwd(), ts.sys.fileExists);
  if (configPath) {
    const config = ts.readConfigFile(configPath, ts.sys.readFile);
    const parsed = ts.parseJsonConfigFileContent(config.config || {}, ts.sys, path.dirname(configPath));
    const options = { ...parsed.options, noEmit: true, incremental: false, composite: false };
    const candidates = new Map(files.map(f => [path.resolve(f.file), f.content]));
    const roots = [...new Set([...parsed.fileNames, ...candidates.keys()])];
    const key = d => (d.file ? path.resolve(d.file.fileName) : '') + ':' + d.code + ':' + ts.flattenDiagnosticMessageText(d.messageText, ' ');
    const diagnostics = overlay => {
      const host = ts.createCompilerHost(options);
      const read = host.readFile.bind(host);
      host.readFile = file => overlay && candidates.has(path.resolve(file)) ? candidates.get(path.resolve(file)) : read(file);
      return ts.getPreEmitDiagnostics(ts.createProgram(roots, options, host));
    };
    const baseline = new Map();
    for (const d of diagnostics(false)) baseline.set(key(d), (baseline.get(key(d)) || 0) + 1);
    for (const d of diagnostics(true)) {
      const k = key(d), count = baseline.get(k) || 0;
      if (count) { baseline.set(k, count-1); continue; }
      const loc = d.file ? d.file.getLineAndCharacterOfPosition(d.start || 0) : {line:0, character:0};
      console.log((d.file ? d.file.fileName : 'configuration') + ':' + (loc.line+1) + ':' + (loc.character+1) + ': TS' + d.code + ' ' + ts.flattenDiagnosticMessageText(d.messageText, ' '));
      failed = true;
    }
  }
}
process.exitCode = failed ? 1 : 0;
`

const pythonCandidateScript = `import json, sys
failed = False
for f in json.load(sys.stdin):
    try:
        compile(f['content'], f['file'], 'exec', dont_inherit=True)
    except (SyntaxError, ValueError) as e:
        print('%s:%s: %s' % (f['file'], getattr(e, 'lineno', 0), e))
        failed = True
sys.exit(1 if failed else 0)
`

func (a *App) checkProposalSyntax(ctx context.Context, proposal model.RepairProposal) error {
	if len(proposal.Edits) == 0 {
		return nil
	}
	preview, err := patch.PreviewEdits(a.Root, proposal.Edits)
	if err != nil {
		return err
	}
	type candidate struct {
		File    string `json:"file"`
		Content string `json:"content"`
	}
	var files, pythonFiles []candidate
	for _, file := range preview {
		switch filepath.Ext(file.File) {
		case ".go":
			if _, err := parser.ParseFile(token.NewFileSet(), file.File, file.After, parser.AllErrors); err != nil {
				return fmt.Errorf("candidate Go syntax check failed: %w", err)
			}
		case ".json":
			if !json.Valid([]byte(file.After)) {
				return fmt.Errorf("candidate JSON syntax check failed: %s", file.File)
			}
		case ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs":
			files = append(files, candidate{file.File, file.After})
		case ".py", ".pyi":
			pythonFiles = append(pythonFiles, candidate{file.File, file.After})
		}
	}
	if len(pythonFiles) > 0 {
		tools := a.Tools
		if tools == nil {
			tools = detector.SystemTools{}
		}
		python := tools.Python(a.Root)
		if len(python) == 0 {
			return fmt.Errorf("candidate Python validation requires a Python 3 interpreter")
		}
		input, err := json.Marshal(pythonFiles)
		if err != nil {
			return err
		}
		result := a.Runner.RunInput(ctx, a.Root, append(python, "-I", "-S", "-c", pythonCandidateScript), 20*time.Second, string(input))
		if result.ExitCode != 0 || result.Truncated {
			return fmt.Errorf("candidate Python syntax check failed: %s", security.Redact(result.Stdout+result.Stderr))
		}
	}
	if len(files) == 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(a.Root, "node_modules", "typescript", "package.json")); err != nil {
		return nil
	}
	input, err := json.Marshal(files)
	if err != nil {
		return err
	}
	a.UI.Say("Checking proposed TypeScript syntax and new compiler diagnostics in memory...")
	r := a.Runner
	r.Remove = append(append([]string(nil), r.Remove...), "NODE_OPTIONS")
	result := r.RunInput(ctx, a.Root, []string{"node", "-e", typescriptSyntaxScript}, 30*time.Second, string(input))
	if result.ExitCode != 0 || result.TimedOut || result.Truncated {
		detail := security.Redact(result.Stdout + result.Stderr)
		if len(detail) > 1500 {
			detail = detail[:1500]
		}
		return fmt.Errorf("candidate TypeScript validation failed (exit %d): %s", result.ExitCode, strings.TrimSpace(detail))
	}
	return nil
}

package detector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pushguard/pushguard/internal/model"
)

// Include existing CI variants while avoiding interactive or modifying script
// names. Package scripts remain repository-owned code, shown before execution.
func safeCheckScript(name string) bool {
	parts := strings.Split(name, ":")
	if len(parts) < 2 {
		return false
	}
	switch parts[0] {
	case "lint", "typecheck", "test", "build":
	default:
		return false
	}
	for _, p := range parts[1:] {
		switch p {
		case "watch", "fix", "update", "dev", "serve", "start", "deploy", "publish", "write":
			return false
		}
	}
	return true
}

func nodeManager(root string) string {
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var manifest struct {
			PackageManager string `json:"packageManager"`
		}
		if json.Unmarshal(data, &manifest) == nil && manifest.PackageManager != "" {
			name := manifest.PackageManager
			if at := strings.IndexByte(name, '@'); at > 0 {
				name = name[:at]
			}
			switch name {
			case "npm", "pnpm", "yarn", "bun":
				return name
			}
		}
	}
	for _, pair := range []struct{ name, file string }{{"pnpm", "pnpm-lock.yaml"}, {"yarn", "yarn.lock"}, {"bun", "bun.lockb"}} {
		if fileExists(filepath.Join(root, pair.file)) {
			return pair.name
		}
	}
	if fileExists(filepath.Join(root, "bun.lock")) {
		return "bun"
	}
	return "npm"
}

func nodeScripts(root string) map[string]bool {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil
	}
	var v struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &v) != nil {
		return nil
	}
	out := map[string]bool{}
	for k := range v.Scripts {
		out[k] = true
	}
	return out
}

func makeTargets(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil || len(data) > 1<<20 {
		return nil
	}
	wanted := []string{"lint", "typecheck", "check", "test", "build"}
	found := map[string]bool{}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ".PHONY:") {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 || strings.ContainsAny(line[:colon], " $()") {
			continue
		}
		for _, target := range strings.Fields(line[:colon]) {
			for _, wanted := range wanted {
				if target == wanted {
					found[target] = true
				}
			}
		}
	}
	var out []string
	for _, target := range wanted {
		if found[target] {
			out = append(out, target)
		}
	}
	return out
}

func mavenExecutable(root string) string {
	if runtime.GOOS == "windows" && fileExists(filepath.Join(root, "mvnw.cmd")) {
		return "mvnw.cmd"
	}
	if runtime.GOOS != "windows" && fileExists(filepath.Join(root, "mvnw")) {
		return "./mvnw"
	}
	return "mvn"
}

func gradleExecutable(root string) string {
	if runtime.GOOS == "windows" && fileExists(filepath.Join(root, "gradlew.bat")) {
		return "gradlew.bat"
	}
	if runtime.GOOS != "windows" && fileExists(filepath.Join(root, "gradlew")) {
		return "./gradlew"
	}
	return "gradle"
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
func Names(checks []model.Check) []string {
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = c.Name
	}
	return out
}

func hasText(path, pattern string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), pattern)
}

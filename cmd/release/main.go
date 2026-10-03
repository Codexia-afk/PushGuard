// release produces dependency-free, reproducible binaries and installation archives.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"debug/macho"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/model"
)

func main() {
	output := flag.String("output", "dist", "Output directory")
	flag.Parse()
	if err := build(*output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func build(output string) error {
	if err := os.MkdirAll(output, 0755); err != nil {
		return err
	}
	commit, built := buildMetadata()
	temp, err := os.MkdirTemp("", "pushguard-release-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	var sums []string
	for _, target := range []struct{ os, arch string }{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"}} {
		name := "pushguard"
		if target.os == "windows" {
			name += ".exe"
		}
		binary := filepath.Join(temp, name)
		ldflags := fmt.Sprintf("-s -w -X github.com/pushguard/pushguard/internal/model.BuildCommit=%s -X github.com/pushguard/pushguard/internal/model.BuildDate=%s", commit, built)
		cmd := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-ldflags="+ldflags, "-o", binary, "./cmd/pushguard")
		cmd.Env = append(os.Environ(), "GOOS="+target.os, "GOARCH="+target.arch, "CGO_ENABLED=0")
		cmd.Stderr = os.Stderr
		if err = cmd.Run(); err != nil {
			return fmt.Errorf("%s/%s: %w", target.os, target.arch, err)
		}
		if target.os == "darwin" {
			if err = requireMacOSUUID(binary); err != nil {
				return err
			}
		}
		archive := filepath.Join(output, "pushguard_"+model.Version+"_"+target.os+"_"+target.arch)
		entries := []entry{{name, binary, 0755}, {"README.md", "README.md", 0644}, {"LICENSE", "LICENSE", 0644}, {"docs/architecture.md", "docs/architecture.md", 0644}, {"docs/security.md", "docs/security.md", 0644}, {"docs/testing.md", "docs/testing.md", 0644}, {"docs/requirements.md", "docs/requirements.md", 0644}}
		hostedName := "pushguard-hosted"
		if target.os == "windows" {
			hostedName += ".exe"
		}
		hostedBinary := filepath.Join(temp, hostedName)
		hosted := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-ldflags="+ldflags, "-o", hostedBinary, "./cmd/pushguard-hosted")
		hosted.Env = cmd.Env
		hosted.Stderr = os.Stderr
		if err = hosted.Run(); err != nil {
			return fmt.Errorf("hosted publisher %s/%s: %w", target.os, target.arch, err)
		}
		if target.os == "darwin" {
			if err = requireMacOSUUID(hostedBinary); err != nil {
				return err
			}
		}
		entries = append(entries, entry{hostedName, hostedBinary, 0755})
		for _, doc := range []string{"ai-repair.md", "ollama.md", "github-integration.md", "pr-workflow.md", "verification-receipts.md", "github-checks.md", "security-model.md", "pr-acceptance.md"} {
			path := "docs/" + doc
			entries = append(entries, entry{path, path, 0644})
		}
		if target.os == "windows" {
			archive += ".zip"
			err = zipFiles(archive, entries)
		} else {
			archive += ".tar.gz"
			err = tarFiles(archive, entries)
		}
		if err != nil {
			return err
		}
		sums, err = appendChecksum(sums, archive)
		if err != nil {
			return err
		}
		stable := filepath.Join(output, stableArtifact(target.os, target.arch, target.os == "windows"))
		if err = copyFile(archive, stable); err != nil {
			return err
		}
		sums, err = appendChecksum(sums, stable)
		if err != nil {
			return err
		}
		fmt.Println("Built", archive)
	}
	sort.Strings(sums)
	data := []byte(strings.Join(sums, "\n") + "\n")
	if err := os.WriteFile(filepath.Join(output, "SHA256SUMS"), data, 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(output, "checksums.txt"), data, 0644)
}

func buildMetadata() (string, string) {
	commit := os.Getenv("PUSHGUARD_BUILD_COMMIT")
	if commit == "" {
		git := "git"
		if runtime.GOOS == "darwin" {
			if path, err := exec.LookPath("git"); err == nil && filepath.Clean(path) == "/usr/bin/git" {
				for _, candidate := range []string{"/Library/Developer/CommandLineTools/usr/bin/git", "/Applications/Xcode.app/Contents/Developer/usr/bin/git"} {
					if _, statErr := os.Stat(candidate); statErr == nil {
						if exec.Command(candidate, "--version").Run() == nil {
							git = candidate
							break
						}
					}
				}
			}
		}
		if output, err := exec.Command(git, "rev-parse", "--short=12", "HEAD").Output(); err == nil {
			commit = strings.TrimSpace(string(output))
		}
	}
	if commit == "" {
		commit = "dev"
	}
	built := os.Getenv("PUSHGUARD_BUILD_DATE")
	if built == "" {
		if epoch := os.Getenv("SOURCE_DATE_EPOCH"); epoch != "" {
			if seconds, err := strconv.ParseInt(epoch, 10, 64); err == nil {
				built = time.Unix(seconds, 0).UTC().Format(time.RFC3339)
			}
		}
	}
	if built == "" {
		built = time.Now().UTC().Format(time.RFC3339)
	}
	return commit, built
}

func stableArtifact(goos, arch string, windows bool) string {
	extension := ".tar.gz"
	if windows {
		extension = ".zip"
	}
	return fmt.Sprintf("pushguard-%s-%s%s", goos, arch, extension)
}

func appendChecksum(sums []string, path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return sums, err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, f)
	closeErr := f.Close()
	if copyErr != nil {
		return sums, copyErr
	}
	if closeErr != nil {
		return sums, closeErr
	}
	return append(sums, fmt.Sprintf("%x  %s", hash.Sum(nil), filepath.Base(path))), nil
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// macOS 26 rejects executables without LC_UUID. Check both architectures even
// when archives are built on Linux, where executing a Mach-O is impossible.
func requireMacOSUUID(path string) error {
	f, err := macho.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, load := range f.Loads {
		raw := load.Raw()
		if len(raw) >= 24 && f.ByteOrder.Uint32(raw[:4]) == 0x1b {
			return nil
		}
	}
	return fmt.Errorf("macOS release lacks LC_UUID; build releases with Go 1.24+ and preserve the Go build ID")
}

type entry struct {
	name, path string
	mode       int64
}

func tarFiles(path string, entries []entry) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, item := range entries {
		data, err := os.ReadFile(item.path)
		if err != nil {
			return err
		}
		if err = tw.WriteHeader(&tar.Header{Name: item.name, Mode: item.mode, Size: int64(len(data)), ModTime: time.Unix(0, 0)}); err != nil {
			return err
		}
		if _, err = tw.Write(data); err != nil {
			return err
		}
	}
	if err = tw.Close(); err != nil {
		return err
	}
	if err = gz.Close(); err != nil {
		return err
	}
	return f.Close()
}
func zipFiles(path string, entries []entry) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, item := range entries {
		data, err := os.ReadFile(item.path)
		if err != nil {
			return err
		}
		header := &zip.FileHeader{Name: item.name, Method: zip.Deflate}
		header.SetMode(os.FileMode(item.mode))
		header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
		w, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err = w.Write(data); err != nil {
			return err
		}
	}
	if err = zw.Close(); err != nil {
		return err
	}
	return f.Close()
}

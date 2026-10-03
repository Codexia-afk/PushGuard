package receipt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/security"
)

func Base() (string, error) {
	if path := os.Getenv("PUSHGUARD_CACHE_DIR"); path != "" {
		return filepath.Abs(path)
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "pushguard"), nil
}
func repositoryDir(root string) (string, error) {
	base, err := Base()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if real, e := filepath.EvalSymlinks(abs); e == nil {
		abs = real
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	return filepath.Join(base, "receipts", hex.EncodeToString(sum[:])), nil
}

// Save records immutable evidence and a repository-scoped pointer to the latest session.
func Save(report model.SessionReport) (string, error) {
	if report.Root == "" && report.Repository != nil {
		report.Root = report.Repository.Root
	}
	dir, err := repositoryDir(report.Root)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("unsafe receipt directory")
	}
	if err = security.SecureDirectory(dir); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(security.SanitizeReport(report), "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if len(data) > 32<<20 {
		return "", fmt.Errorf("receipt exceeds 32 MiB evidence limit")
	}
	file, err := os.CreateTemp(dir, time.Now().UTC().Format("20060102T150405")+"-*.json")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	closeErr := file.Close()
	if err != nil {
		os.Remove(path)
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	latest, err := os.CreateTemp(dir, ".latest-*")
	if err != nil {
		return "", err
	}
	name := latest.Name()
	defer os.Remove(name)
	if err = latest.Chmod(0600); err == nil {
		_, err = latest.Write(data)
	}
	closeErr = latest.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err = os.Rename(name, filepath.Join(dir, "latest.json")); err != nil {
		return "", err
	}
	return path, nil
}
func Latest(root string) (model.SessionReport, error) {
	var report model.SessionReport
	dir, err := repositoryDir(root)
	if err != nil {
		return report, err
	}
	path := filepath.Join(dir, "latest.json")
	info, err := os.Lstat(path)
	if err != nil {
		return report, err
	}
	if !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return report, fmt.Errorf("invalid receipt")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return report, err
	}
	if err = json.Unmarshal(data, &report); err != nil {
		return report, err
	}
	if report.SchemaVersion != "2" {
		return report, fmt.Errorf("unsupported receipt schema %s", report.SchemaVersion)
	}
	abs, _ := filepath.Abs(root)
	if real, e := filepath.EvalSymlinks(abs); e == nil {
		abs = real
	}
	if filepath.Clean(report.Root) != filepath.Clean(abs) {
		return report, fmt.Errorf("receipt belongs to another repository")
	}
	return report, nil
}

package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/testutil"
)

func TestAIStatusUsesSelectedRootConfigurationAndExplicitGeneration(t *testing.T) {
	root := configured(t)
	testutil.Write(t, root, "src/file.ts", "export const value = 1;\n")
	var generations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"code:local"}]}`))
		case "/api/chat":
			generations.Add(1)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			data, _ := json.Marshal(body)
			if strings.Contains(string(data), "src/file.ts") {
				t.Error("status generation sent source context")
			}
			_, _ = w.Write([]byte(`{"message":{"content":"{\"summary\":\"probe\",\"rootCause\":\"not applicable\"}"}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 500*time.Millisecond); err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("skipping test requiring loopback socket in restricted sandbox: %v", err)
		}
	} else {
		_ = conn.Close()
	}
	cfg, _, _ := config.Load(root)
	cfg.AI.Provider, cfg.AI.Model, cfg.AI.Endpoint = "ollama", "code:local", server.URL
	data, _ := json.Marshal(cfg)
	testutil.Write(t, root, ".pushguard.json", string(data))
	for _, test := range []bool{false, true} {
		args := []string{"ai", "status", "--repo", filepath.Join(root, "src"), "--json"}
		if test {
			args = append(args, "--test")
		}
		code, out := cli(t, "", args...)
		if code != 0 || !json.Valid([]byte(out)) || !strings.Contains(out, "code:local") {
			t.Fatalf("AI status failed: %d %s", code, out)
		}
		want := int32(0)
		if test {
			want = 1
		}
		if generations.Load() != want {
			t.Fatal("generation ran without explicit test")
		}
	}
}

func TestAIStatusExplainsExplicitDisable(t *testing.T) {
	root := configured(t)
	code, out := cli(t, "", "ai", "status", "--repo", root)
	if code != 7 || !strings.Contains(out, "Repository configuration overrides user-wide defaults") || !strings.Contains(out, ".pushguard.json") {
		t.Fatalf("unhelpful AI status: %d %s", code, out)
	}
}

func TestInitPreservesConfiguredUserProvider(t *testing.T) {
	root, _ := testutil.Repository(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"ai":{"provider":"ollama","model":"installed-code"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PUSHGUARD_CONFIG_FILE", path)
	if code, out := cli(t, "", "init", "--repo", root); code != 0 {
		t.Fatalf("init failed: %d %s", code, out)
	}
	cfg, selected, err := config.Load(root)
	if err != nil || cfg.AI.Model != "installed-code" || selected != filepath.Join(root, ".pushguard.json") {
		t.Fatal("init disabled configured provider")
	}
}

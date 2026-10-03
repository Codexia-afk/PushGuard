package detector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/config"
)

func TestNodeCIVariantsAreOrderedAndDoNotInventPytest(t *testing.T) {
	root := t.TempDir()
	data := `{"packageManager":"pnpm@10","scripts":{"lint":"eslint .","lint:ci":"eslint .","lint:fix":"eslint --fix .","test":"vitest run","test:unit":"vitest run unit","test:watch":"vitest","build":"tsc","build:deploy":"deploy"}}`
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "tests"), 0700); err != nil {
		t.Fatal(err)
	}
	checks, err := Checks(root, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(Names(checks), ","); got != "lint,lint:ci,test,test:unit,build" {
		t.Fatal(got)
	}
	for _, c := range checks {
		if c.Args[0] != "pnpm" {
			t.Fatal(c.Args)
		}
	}
}

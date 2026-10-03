package receipt

import (
	"os"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestSaveReceipt(t *testing.T) {
	t.Setenv("PUSHGUARD_CACHE_DIR", t.TempDir())
	path, err := Save(model.SessionReport{SchemaVersion: "1", Status: model.StatusPass})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

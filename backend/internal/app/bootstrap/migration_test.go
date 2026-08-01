package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrationPublishRollsBackPartialFiles(t *testing.T) {
	root := t.TempDir()
	firstTemp := filepath.Join(root, "first.tmp")
	secondTemp := filepath.Join(root, "second.tmp")
	firstFinal := filepath.Join(root, "first.final")
	if os.WriteFile(firstTemp, []byte("first"), 0o600) != nil || os.WriteFile(secondTemp, []byte("second"), 0o600) != nil {
		t.Fatal("fixture write failed")
	}
	err := publishMigration([]stagedMigration{
		{firstTemp, firstFinal},
		{secondTemp, filepath.Join(root, "missing", "second.final")},
	})
	if err == nil {
		t.Fatal("injected publish failure was ignored")
	}
	if _, err := os.Stat(firstFinal); !os.IsNotExist(err) {
		t.Fatalf("partial migration file was not rolled back: %v", err)
	}
}

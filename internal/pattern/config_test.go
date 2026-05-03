package pattern

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadConfigSeedsMissingFiles checks the missing-file case: each
// embedded default is materialized in the config dir and the in-memory
// library is rebuilt from those copies.
func TestLoadConfigSeedsMissingFiles(t *testing.T) {
	dir := t.TempDir()
	var warn bytes.Buffer

	if err := LoadConfig(dir, &warn); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	for _, name := range allEmbeddedFiles {
		path := filepath.Join(dir, name)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("expected seeded file %s: %v", path, err)
		}
		want, err := readEmbedded(name)
		if err != nil {
			t.Fatalf("readEmbedded(%s): %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("seeded file %s does not match embedded default", path)
		}
	}

	if warn.Len() != 0 {
		t.Errorf("unexpected warning on first seed: %s", warn.String())
	}
	if len(KnownPatterns) == 0 {
		t.Errorf("KnownPatterns empty after LoadConfig")
	}
}

// TestLoadConfigUsesDiskCopy verifies that when a config file exists on
// disk, its contents are loaded (replacing the embedded default).
func TestLoadConfigUsesDiskCopy(t *testing.T) {
	t.Cleanup(restoreEmbeddedDefaults(t))

	dir := t.TempDir()
	custom := []KnownPattern{
		{
			Name:        "Custom Test Pattern",
			Pattern:     `%{TIMESTAMP_ISO8601:timestamp} CUSTOM %{GREEDYDATA:message}`,
			Priority:    1,
			Specificity: 99,
		},
	}
	mustWriteJSON(t, filepath.Join(dir, fileNamePatterns), custom)
	mustCopyEmbedded(t, dir, fileNamePrimitives)

	if err := LoadConfig(dir, nil); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(KnownPatternsLibrary) != 1 || KnownPatternsLibrary[0].Name != "Custom Test Pattern" {
		t.Fatalf("disk copy not applied: %+v", KnownPatternsLibrary)
	}
	found := false
	for _, kp := range KnownPatterns {
		if kp.Name == "Custom Test Pattern" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("custom pattern missing from composed KnownPatterns")
	}
}

// TestLoadConfigRecoversCorruptFile verifies that an unparseable file
// is backed up, replaced with the embedded default, and a warning is
// written.
func TestLoadConfigRecoversCorruptFile(t *testing.T) {
	t.Cleanup(restoreEmbeddedDefaults(t))

	dir := t.TempDir()
	corruptPath := filepath.Join(dir, fileNamePatterns)
	if err := os.WriteFile(corruptPath, []byte("{this is not valid json"), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	var warn bytes.Buffer
	if err := LoadConfig(dir, &warn); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if !strings.Contains(warn.String(), "was corrupt") {
		t.Errorf("expected corruption warning, got: %q", warn.String())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var hasBackup bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), fileNamePatterns+".bak.") {
			hasBackup = true
			break
		}
	}
	if !hasBackup {
		t.Errorf("expected a .bak.<ts> backup of %s under %s", fileNamePatterns, dir)
	}

	got, err := os.ReadFile(corruptPath)
	if err != nil {
		t.Fatalf("re-seeded file missing: %v", err)
	}
	want, err := readEmbedded(fileNamePatterns)
	if err != nil {
		t.Fatalf("readEmbedded: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("re-seeded contents do not match embedded default")
	}
}

// TestResetConfigBacksUpExistingFiles verifies ResetConfig replaces all
// files with embedded defaults and creates backups.
func TestResetConfigBacksUpExistingFiles(t *testing.T) {
	t.Cleanup(restoreEmbeddedDefaults(t))

	dir := t.TempDir()
	if err := LoadConfig(dir, nil); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileNamePrimitives), []byte("{}"), 0o644); err != nil {
		t.Fatalf("overwrite primitives: %v", err)
	}

	if err := ResetConfig(dir, nil); err != nil {
		t.Fatalf("ResetConfig: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var backupCount int
	for _, e := range entries {
		if strings.Contains(e.Name(), ".bak.") {
			backupCount++
		}
	}
	if backupCount != len(allEmbeddedFiles) {
		t.Errorf("expected %d backups, got %d", len(allEmbeddedFiles), backupCount)
	}
	got, err := os.ReadFile(filepath.Join(dir, fileNamePrimitives))
	if err != nil {
		t.Fatalf("read primitives after reset: %v", err)
	}
	want, _ := readEmbedded(fileNamePrimitives)
	if !bytes.Equal(got, want) {
		t.Errorf("primitives.json not restored from embedded default after reset")
	}
}

func mustWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
}

func mustCopyEmbedded(t *testing.T, dir, name string) {
	t.Helper()
	data, err := readEmbedded(name)
	if err != nil {
		t.Fatalf("readEmbedded %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// restoreEmbeddedDefaults returns a cleanup func that resets the package
// vars back to embedded defaults so a test mutating the global library
// does not leak into other tests.
func restoreEmbeddedDefaults(t *testing.T) func() {
	t.Helper()
	return func() {
		if err := loadEmbeddedDefaults(); err != nil {
			t.Fatalf("restore embedded defaults: %v", err)
		}
		RefreshLibrary()
	}
}

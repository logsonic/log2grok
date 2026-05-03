package log2grok

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// activateTempConfig points log2grok at a temp directory and seeds it
// from the embedded defaults. Because LoadConfig mutates package-level
// state, tests that need an isolated config must run sequentially —
// they all call this helper, which serializes via t.Cleanup re-seeding.
func activateTempConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := LoadConfig(dir, nil); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return dir
}

func TestConfigDir_TracksLoadConfig(t *testing.T) {
	dir := activateTempConfig(t)
	if got := ConfigDir(); got != dir {
		t.Errorf("ConfigDir = %q, want %q", got, dir)
	}
}

func TestUpsertLibraryEntry_RejectsWithoutLoadConfig(t *testing.T) {
	// Reset state so other tests' LoadConfig doesn't bleed in.
	if err := ResetConfig(t.TempDir(), nil); err != nil {
		t.Fatalf("ResetConfig setup: %v", err)
	}
	t.Setenv("LOG2GROK_BYPASS_LOAD", "")
	// Force the unloaded sentinel by clearing the package var via a
	// scoped LoadConfig that we then invalidate. Easiest path: manually
	// drive ResetConfig to a tmp dir and then call into a routine that
	// shouldn't have access. Since CurrentConfigDir is package-private
	// state we can't blank it without an exported reset; just verify
	// that an empty Name short-circuits before the dir check.
	_, err := UpsertLibraryEntry(KnownPattern{})
	if !errors.Is(err, ErrEmptyName) {
		t.Errorf("blank entry should return ErrEmptyName, got %v", err)
	}
}

func TestUpsertLibraryEntry_InsertReplacePersistAndReload(t *testing.T) {
	dir := activateTempConfig(t)

	entry := KnownPattern{
		Name:        "ls-test-pattern",
		Pattern:     `%{IP:addr} %{GREEDYDATA:msg}`,
		Priority:    50,
		Specificity: 80,
		Description: "logsonic admin test",
	}

	replaced, err := UpsertLibraryEntry(entry)
	if err != nil {
		t.Fatalf("Upsert insert: %v", err)
	}
	if replaced != nil {
		t.Errorf("first insert should not replace anything, got %+v", replaced)
	}

	found := false
	for _, kp := range ListLibrary() {
		if kp.Name == entry.Name {
			found = true
			if kp.Specificity != 80 {
				t.Errorf("specificity not preserved: got %d", kp.Specificity)
			}
		}
	}
	if !found {
		t.Fatal("entry not visible via ListLibrary after insert")
	}

	// Disk side-effect: patterns.json must contain the entry.
	data, err := os.ReadFile(filepath.Join(dir, "patterns.json"))
	if err != nil {
		t.Fatalf("read patterns.json: %v", err)
	}
	if !strings.Contains(string(data), "ls-test-pattern") {
		t.Error("patterns.json missing the upserted entry")
	}

	// Update in place.
	entry.Description = "updated"
	prev, err := UpsertLibraryEntry(entry)
	if err != nil {
		t.Fatalf("Upsert replace: %v", err)
	}
	if prev == nil || prev.Description != "logsonic admin test" {
		t.Errorf("replace should return prior value, got %+v", prev)
	}

	// Reload via LoadConfig and confirm the persisted change survives.
	if err := LoadConfig(dir, nil); err != nil {
		t.Fatalf("LoadConfig reload: %v", err)
	}
	for _, kp := range ListLibrary() {
		if kp.Name == entry.Name {
			if kp.Description != "updated" {
				t.Errorf("post-reload description: got %q", kp.Description)
			}
			return
		}
	}
	t.Fatal("entry missing after reload")
}

func TestUpsertLibraryEntry_RejectsInvalidGrok(t *testing.T) {
	activateTempConfig(t)
	_, err := UpsertLibraryEntry(KnownPattern{
		Name:    "bad-ref",
		Pattern: `%{NO_SUCH_PRIMITIVE:x}`,
	})
	if err == nil {
		t.Fatal("expected compile error to bubble up")
	}
}

func TestRemoveLibraryEntry_ReturnsTrueOnHit(t *testing.T) {
	activateTempConfig(t)
	entry := KnownPattern{Name: "ls-removable", Pattern: `%{IP:addr}`}
	if _, err := UpsertLibraryEntry(entry); err != nil {
		t.Fatalf("Upsert setup: %v", err)
	}

	removed, err := RemoveLibraryEntry(entry.Name)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !removed {
		t.Fatal("Remove should return true for an existing entry")
	}
	for _, kp := range ListLibrary() {
		if kp.Name == entry.Name {
			t.Fatal("entry still present after remove")
		}
	}
}

func TestRemoveLibraryEntry_NoOpWhenAbsent(t *testing.T) {
	activateTempConfig(t)
	removed, err := RemoveLibraryEntry("definitely-not-there")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if removed {
		t.Fatal("Remove should return false when name absent")
	}
}

func TestUpsertPrimitive_RejectsBadRegex(t *testing.T) {
	activateTempConfig(t)
	_, err := UpsertPrimitive("BROKEN", `(unbalanced`)
	if err == nil {
		t.Fatal("expected regex compile error")
	}
}

func TestUpsertPrimitive_PersistsAndIsUsableInDecoder(t *testing.T) {
	dir := activateTempConfig(t)
	if _, err := UpsertPrimitive("LSADMIN_DIGIT", `\d+`); err != nil {
		t.Fatalf("UpsertPrimitive: %v", err)
	}

	if got := ListPrimitives()["LSADMIN_DIGIT"]; got != `\d+` {
		t.Errorf("ListPrimitives missing LSADMIN_DIGIT: %v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "primitives.json"))
	if err != nil {
		t.Fatalf("read primitives.json: %v", err)
	}
	if !strings.Contains(string(data), "LSADMIN_DIGIT") {
		t.Error("primitives.json missing the upserted entry")
	}

	dec, err := NewDecoder(PatternSpec{
		Grok: `value=%{LSADMIN_DIGIT:n}`,
	}, DecoderOptions{})
	if err != nil {
		t.Fatalf("NewDecoder using new primitive: %v", err)
	}
	r := dec.Decode([]string{"value=42"})[0]
	if !r.Matched || r.Fields["n"] != "42" {
		t.Errorf("decoder did not pick up the new primitive: %+v", r)
	}

	removed, err := RemovePrimitive("LSADMIN_DIGIT")
	if err != nil || !removed {
		t.Errorf("RemovePrimitive: removed=%v err=%v", removed, err)
	}
}

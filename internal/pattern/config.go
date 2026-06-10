package pattern

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// DefaultConfigDirName is the directory created in the current working
// directory when LoadConfig is called without an explicit path.
const DefaultConfigDirName = ".log2grok"

// currentConfigDir holds the directory passed to the most recent
// successful LoadConfig / ResetConfig call. It is consulted by the
// admin APIs (UpsertLibraryEntry, RemoveLibraryEntry, etc.) so they
// know where to persist edits. Empty string means LoadConfig has not
// yet been invoked, in which case admin writes are rejected.
var currentConfigDir string

// CurrentConfigDir returns the directory most recently activated by
// LoadConfig or ResetConfig. Returns "" when no externalized config is
// active (either because the caller never called LoadConfig or because
// the call failed before assigning).
func CurrentConfigDir() string {
	patternStateMu.RLock()
	defer patternStateMu.RUnlock()
	return currentConfigDir
}

// LoadConfig seeds and loads the per-project pattern library from disk.
//
// Behavior, per file:
//
//   - If the file does not exist, it is created from the embedded default.
//   - If the file exists and parses cleanly, its contents replace the
//     in-memory defaults.
//   - If the file exists but is corrupt (parse error or read error), it
//     is renamed to a backup (".bak.<timestamp>"), the embedded default
//     is written in its place, and a warning is emitted to warn (when
//     warn is non-nil). The embedded default is then used.
//
// After all files have been processed, the full primitives + patterns
// snapshot is committed at once so concurrent discovery never observes a
// half-loaded config.
//
// If dir is empty, DefaultConfigDirName under the current working
// directory is used. The directory is created if missing.
func LoadConfig(dir string, warn io.Writer) error {
	if dir == "" {
		dir = DefaultConfigDirName
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("log2grok: create config dir %s: %w", dir, err)
	}

	var (
		primitives map[string]string
		library    []KnownPattern
	)
	for _, name := range allEmbeddedFiles {
		data, err := loadOrSeedBytes(dir, name, warn)
		if err != nil {
			return err
		}
		switch name {
		case fileNamePrimitives:
			primitives, err = decodePrimitives(data)
		case fileNamePatterns:
			library, err = decodePatterns(data)
			FillEmptyDescriptionsInPlace(library)
		default:
			err = fmt.Errorf("unknown config file %q", name)
		}
		if err != nil {
			path := filepath.Join(dir, name)
			data, err = recoverWithBackupBytes(name, path, fmt.Errorf("parse %s: %w", name, err), warn)
			if err != nil {
				return err
			}
			switch name {
			case fileNamePrimitives:
				primitives, err = decodePrimitives(data)
			case fileNamePatterns:
				library, err = decodePatterns(data)
				FillEmptyDescriptionsInPlace(library)
			}
			if err != nil {
				return fmt.Errorf("log2grok: embedded %s failed to parse after recovery: %w", name, err)
			}
		}
	}

	commitPatternState(primitives, library, &dir)
	return nil
}

// ResetConfig forcibly overwrites every file under dir with the embedded
// default. Existing files are renamed to ".bak.<timestamp>" first.
// RefreshLibrary is called at the end.
func ResetConfig(dir string, warn io.Writer) error {
	if dir == "" {
		dir = DefaultConfigDirName
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("log2grok: create config dir %s: %w", dir, err)
	}
	for _, name := range allEmbeddedFiles {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			if _, berr := backupFile(path); berr != nil {
				return fmt.Errorf("log2grok: backup %s: %w", path, berr)
			}
		}
		if err := writeEmbeddedTo(name, path); err != nil {
			return fmt.Errorf("log2grok: seed %s: %w", path, err)
		}
		if warn != nil {
			fmt.Fprintf(warn, "log2grok: reset %s from embedded default\n", path)
		}
	}
	primitives, library, err := embeddedDefaults()
	if err != nil {
		return err
	}
	commitPatternState(primitives, library, &dir)
	return nil
}

// loadOrSeedBytes handles a single file according to the LoadConfig
// contract and returns the bytes to parse. It does not mutate package state.
func loadOrSeedBytes(dir, name string, warn io.Writer) ([]byte, error) {
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if werr := writeEmbeddedTo(name, path); werr != nil {
			return nil, fmt.Errorf("log2grok: seed %s: %w", path, werr)
		}
		return mustReadEmbedded(name), nil
	}
	if err != nil {
		// A read error (permissions, I/O) is treated as corruption: back
		// up whatever is there, re-seed, and continue.
		return recoverWithBackupBytes(name, path, fmt.Errorf("read: %w", err), warn)
	}
	return data, nil
}

// recoverWithBackup renames path to a timestamped .bak file, writes the
// embedded default to path, emits a warning, and returns the embedded bytes.
// Returns any unrecoverable error.
func recoverWithBackupBytes(name, path string, cause error, warn io.Writer) ([]byte, error) {
	backup, berr := backupFile(path)
	if berr != nil && !errors.Is(berr, os.ErrNotExist) {
		return nil, fmt.Errorf("log2grok: backup %s: %w (original error: %v)", path, berr, cause)
	}
	if werr := writeEmbeddedTo(name, path); werr != nil {
		return nil, fmt.Errorf("log2grok: re-seed %s: %w (original error: %v)", path, werr, cause)
	}
	if warn != nil {
		fmt.Fprintf(warn, "log2grok: %s was corrupt (%v); backed up to %s, restored embedded default\n",
			path, cause, backup)
	}
	return mustReadEmbedded(name), nil
}

// writeEmbeddedTo writes the embedded default for name to path with mode
// 0o644. The parent directory must already exist.
func writeEmbeddedTo(name, path string) error {
	data, err := readEmbedded(name)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// backupFile renames path to "<path>.bak.<unix-nanos>" and returns the
// backup path. If path does not exist, returns os.ErrNotExist.
func backupFile(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	backup := fmt.Sprintf("%s.bak.%d", path, time.Now().UnixNano())
	if err := os.Rename(path, backup); err != nil {
		return "", err
	}
	return backup, nil
}

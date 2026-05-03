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
// After all files have been processed, RefreshLibrary is called so the
// derived structures and compiled-library cache reflect the new contents.
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

	// Process each file independently. A failure to parse is recovered
	// with the embedded default; a failure to write is fatal because the
	// user explicitly requested an externalized config.
	for _, name := range allEmbeddedFiles {
		if err := loadOrSeed(dir, name, warn); err != nil {
			return err
		}
	}

	RefreshLibrary()
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
	if err := loadEmbeddedDefaults(); err != nil {
		return err
	}
	RefreshLibrary()
	return nil
}

// loadOrSeed handles a single file according to the LoadConfig contract.
// It updates the relevant package-level var on success.
func loadOrSeed(dir, name string, warn io.Writer) error {
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if werr := writeEmbeddedTo(name, path); werr != nil {
			return fmt.Errorf("log2grok: seed %s: %w", path, werr)
		}
		data = mustReadEmbedded(name)
		return applyConfigBytes(name, data)
	}
	if err != nil {
		// A read error (permissions, I/O) is treated as corruption: back
		// up whatever is there, re-seed, and continue.
		return recoverWithBackup(name, path, fmt.Errorf("read: %w", err), warn)
	}
	if perr := applyConfigBytes(name, data); perr != nil {
		return recoverWithBackup(name, path, perr, warn)
	}
	return nil
}

// recoverWithBackup renames path to a timestamped .bak file, writes the
// embedded default to path, applies the embedded default in memory, and
// emits a warning. Returns any unrecoverable error.
func recoverWithBackup(name, path string, cause error, warn io.Writer) error {
	backup, berr := backupFile(path)
	if berr != nil && !errors.Is(berr, os.ErrNotExist) {
		return fmt.Errorf("log2grok: backup %s: %w (original error: %v)", path, berr, cause)
	}
	if werr := writeEmbeddedTo(name, path); werr != nil {
		return fmt.Errorf("log2grok: re-seed %s: %w (original error: %v)", path, werr, cause)
	}
	if warn != nil {
		fmt.Fprintf(warn, "log2grok: %s was corrupt (%v); backed up to %s, restored embedded default\n",
			path, cause, backup)
	}
	return applyConfigBytes(name, mustReadEmbedded(name))
}

// applyConfigBytes parses data for the file kind named by name and stores
// it into the corresponding package-level var. It does not call
// RefreshLibrary; the caller is responsible.
func applyConfigBytes(name string, data []byte) error {
	switch name {
	case fileNamePrimitives:
		v, err := decodePrimitives(data)
		if err != nil {
			return fmt.Errorf("parse %s: %w", name, err)
		}
		GrokPrimitives = v
		GrokPrimitivesOverrides = GrokPrimitives
	case fileNamePatterns:
		v, err := decodePatterns(data)
		if err != nil {
			return fmt.Errorf("parse %s: %w", name, err)
		}
		KnownPatternsLibrary = v
	default:
		return fmt.Errorf("unknown config file %q", name)
	}
	return nil
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

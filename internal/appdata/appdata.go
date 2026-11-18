// Package appdata resolves the ~/.tape2digi directory and provides atomic JSON files.
package appdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DirName is the folder in the user's home that holds all app-level data.
const DirName = ".tape2digi"

// Dir is the app data directory.
type Dir struct{ Path string }

// Default returns the ~/.tape2digi directory (not created yet).
func Default() (Dir, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Dir{}, fmt.Errorf("cannot resolve home directory: %w", err)
	}
	return Dir{Path: filepath.Join(home, DirName)}, nil
}

// Ensure creates the directory with user-only permissions.
func (d Dir) Ensure() error { return os.MkdirAll(d.Path, 0o700) }

// File returns the path of a file inside the directory.
func (d Dir) File(name string) string { return filepath.Join(d.Path, name) }

// ReadJSON decodes path into v. A missing file returns os.ErrNotExist.
func ReadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

// WriteJSON atomically replaces path with the JSON encoding of v.
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(b, '\n'), 0o600)
}

// WriteFileAtomic writes data to a temporary file and renames it over path.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// IsNotExist reports whether err means the file is absent.
func IsNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }

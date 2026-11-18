// Package jsonfile keeps a value in a JSON file that other processes read
// and change. A write replaces the file whole, so a reader, or the next
// start after a crash, finds the old file or the new one.
package jsonfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Load reads path into v. A missing file leaves v as it is.
func Load(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Write replaces path with v through a temp file, which CreateTemp makes
// 0600: these files hold tokens.
func Write(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

// Edit changes path for every process that edits it: it waits for path's
// lock, loads what the others wrote, applies change and writes the result,
// which it returns. An error from change leaves the file as it was.
func Edit[T any](path string, change func(*T) error) (T, error) {
	return edit(path, change, false)
}

// EditOrReset is Edit, but a file that doesn't parse as T is, under the
// lock, a zero T for change to start anew from.
func EditOrReset[T any](path string, change func(*T) error) (T, error) {
	return edit(path, change, true)
}

func edit[T any](path string, change func(*T) error, reset bool) (T, error) {
	var v T
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return v, err
	}
	lk, err := lock(path + ".lock")
	if err != nil {
		return v, err
	}
	defer lk.Close()
	if err := Load(path, &v); err != nil {
		syntax, kind := (*json.SyntaxError)(nil), (*json.UnmarshalTypeError)(nil)
		if !reset || !errors.As(err, &syntax) && !errors.As(err, &kind) {
			return v, err
		}
		var zero T
		v = zero
	}
	if err := change(&v); err != nil {
		return v, err
	}
	return v, Write(path, v)
}

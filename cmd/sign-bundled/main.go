// Command sign-bundled signs Glitched's built-in plugins with its release
// key, after one of them changed:
//
//	GLITCHED_RELEASE_KEY=path/to/release-key go run ./cmd/sign-bundled
//
// With -new it makes a release key at that path first, and writes its
// public half into internal/plugins/bundled/bundled.go. A user's copy of a
// built-in is signed with their device's key, so a new release key changes
// nothing that is installed.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"glitched/internal/plugins"
	"glitched/internal/plugins/pack"
)

const dir = "internal/plugins/bundled"

func main() {
	fresh := flag.Bool("new", false, "make a new release key, and compile its public half in")
	flag.Parse()
	path := os.Getenv("GLITCHED_RELEASE_KEY")
	if path == "" {
		fail("set GLITCHED_RELEASE_KEY to the release key's file")
	}
	if *fresh {
		if _, err := os.Stat(path); err == nil {
			fail(path + " exists; move it away to make a new key")
		}
	}
	// LoadKey makes the key on first use, as a device's: in a folder of
	// its own, as plugin-key.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fail(err.Error())
	}
	key, err := loadKey(path, *fresh)
	if err != nil {
		fail(err.Error())
	}
	if *fresh {
		src := filepath.Join(dir, "bundled.go")
		b, err := os.ReadFile(src)
		if err != nil {
			fail(err.Error())
		}
		b = regexp.MustCompile(`const ReleaseKey = "[^"]*"`).ReplaceAll(b, []byte(`const ReleaseKey = "`+key.Public()+`"`))
		if err := os.WriteFile(src, b, 0o644); err != nil {
			fail(err.Error())
		}
	}
	if err := sign(dir, key); err != nil {
		fail(err.Error())
	}
}

// sign signs each plugin in dir with key, and refuses one that isn't
// TypeScript.
func sign(dir string, key *pack.Key) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		plugin := filepath.Join(dir, e.Name())
		if v := plugins.CheckTypeScriptPlugin(plugin); len(v) > 0 {
			return fmt.Errorf("%s is not TypeScript: %v", e.Name(), v)
		}
		if err := pack.Sign(plugin, key); err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
		fmt.Println("signed", e.Name(), "with key", key.ID())
	}
	return nil
}

// loadKey reads the release key at path, or makes it when fresh.
func loadKey(path string, fresh bool) (*pack.Key, error) {
	tmp, err := os.MkdirTemp(filepath.Dir(path), ".release-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if !fresh {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(tmp, "plugin-key"), b, 0o600); err != nil {
			return nil, err
		}
	}
	key, err := pack.LoadKey(tmp)
	if err != nil {
		return nil, err
	}
	if fresh {
		b, err := os.ReadFile(filepath.Join(tmp, "plugin-key"))
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return nil, err
		}
	}
	return key, nil
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "sign-bundled:", msg)
	os.Exit(1)
}

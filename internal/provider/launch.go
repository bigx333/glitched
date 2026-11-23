package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Launch is how the user has this machine's engine start a provider's
// program, as engine.json's providers keep it and Settings › Providers
// edits it. The zero Launch runs the program found on PATH, in its own
// default folder, with no arguments of the user's.
type Launch struct {
	// Path is the program, absolute or under ~. "" finds it.
	Path string `json:"path,omitempty"`
	// Home is the program's own folder of config, sign-in and sessions,
	// absolute or under ~, which every launch names in the variable its
	// Placement's HomeVar is. "" leaves the program its default.
	Home string `json:"home,omitempty"`
	// Args are more arguments for every launch, in shell quoting.
	Args string `json:"args,omitempty"`
}

// Placement is what a provider's program takes of a Launch.
type Placement struct {
	// Names are the program's file names, looked for on PATH, then in
	// ~/.local/bin, where the CLIs' installers put them.
	Names []string
	// HomeVar is the variable that moves the program's folder, and
	// DefaultHome that folder when it is unset. A program with no such
	// variable takes no Home.
	HomeVar, DefaultHome string
	// Reserved are the arguments Glitched sets itself, which Args may not.
	Reserved []Reserved
}

// Reserved is a flag Glitched sets itself: any of Flags, alone or with
// =value, or, with Keys, any of Flags whose key=value overrides one of
// Keys or a key beneath it.
type Reserved struct {
	Flags []string
	Keys  []string
}

// Program is a Launch as a Placement checked it: what a launch starts.
type Program struct {
	Path string   // absolute
	Env  []string // NAME=value, set over what the engine has
	Args []string // the user's own, in order
}

// ErrNoProgram is Program's error when the Launch names no program and
// none of the Placement's names is installed.
var ErrNoProgram = errors.New("provider: the program is not installed")

// LaunchError is what is wrong with a Launch, setting by setting: each
// field that isn't "" says why that one can't be used.
type LaunchError struct {
	Path string `json:"path,omitempty"`
	Home string `json:"home,omitempty"`
	Args string `json:"args,omitempty"`
}

func (e *LaunchError) Error() string {
	var out []string
	for _, f := range []struct{ name, msg string }{{"path", e.Path}, {"home", e.Home}, {"args", e.Args}} {
		if f.msg != "" {
			out = append(out, f.name+": "+f.msg)
		}
	}
	return strings.Join(out, "; ")
}

// Program checks l and finds what it starts. A LaunchError says what is
// wrong with l; ErrNoProgram that l names no program and none is found.
func (p Placement) Program(l Launch) (Program, error) {
	var bad LaunchError
	var prog Program
	if l.Path == "" {
		prog.Path = p.find()
	} else if path, msg := absolute(l.Path); msg != "" {
		bad.Path = msg
	} else if msg := program(path); msg != "" {
		bad.Path = msg
	} else {
		prog.Path = path
	}
	switch home, msg := absolute(l.Home); {
	case l.Home == "":
	case p.HomeVar == "":
		bad.Home = "this program has no folder of its own to move"
	case msg != "":
		bad.Home = msg
	default:
		if fi, err := os.Stat(home); err != nil {
			bad.Home = "there is no folder at " + home
		} else if !fi.IsDir() {
			bad.Home = home + " is a file, not a folder"
		} else {
			prog.Env = []string{p.HomeVar + "=" + home}
		}
	}
	args, err := Split(l.Args)
	if err != nil {
		bad.Args = "the quoting doesn't close: " + err.Error()
	} else if msg := p.reserved(args); msg != "" {
		bad.Args = msg
	} else {
		prog.Args = args
	}
	if bad != (LaunchError{}) {
		return Program{}, &bad
	}
	if prog.Path == "" {
		return Program{}, ErrNoProgram
	}
	return prog, nil
}

func (p Placement) find() string {
	for _, name := range p.Names {
		if path, err := exec.LookPath(name); err == nil {
			if abs, err := filepath.Abs(path); err == nil {
				return abs
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, name := range p.Names {
			if path := filepath.Join(home, ".local", "bin", name); program(path) == "" {
				return path
			}
		}
	}
	return ""
}

// absolute is path with a leading ~ for the home directory, or why it
// can't be used: the engine starts programs in each run's workspace,
// where a relative path names another file.
func absolute(path string) (string, string) {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "no home folder to expand ~ in: " + err.Error()
		}
		path = filepath.Join(home, path[1:])
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Sprintf("%q is relative: give the whole path, from / or ~", path)
	}
	return filepath.Clean(path), ""
}

// program is why path is no program exec can start, or "".
func program(path string) string {
	fi, err := os.Stat(path)
	switch {
	case err != nil:
		return "there is no file at " + path
	case fi.IsDir():
		return path + " is a folder, not a program"
	}
	if _, err := exec.LookPath(path); err != nil {
		return path + " isn't executable"
	}
	return ""
}

// reserved is why args set what Glitched sets itself, or "".
func (p Placement) reserved(args []string) string {
	for i, arg := range args {
		if arg == "--" {
			return "-- would make the flags Glitched adds after it plain arguments"
		}
		for _, r := range p.Reserved {
			for _, f := range r.Flags {
				value, ok, next := given(arg, f)
				if !ok {
					continue
				}
				if len(r.Keys) == 0 {
					return f + " is set by Glitched itself"
				}
				if next {
					if i+1 >= len(args) {
						continue
					}
					value = args[i+1]
				}
				key := configKey(value)
				for _, k := range r.Keys {
					if key == k || strings.HasPrefix(key, k+".") {
						return fmt.Sprintf("%s %s is set by Glitched itself", f, k)
					}
				}
			}
		}
	}
	return ""
}

// given is whether arg gives flag f, and its value: in the next
// argument, after =, or, for a short flag, right after it.
func given(arg, f string) (value string, ok, next bool) {
	switch {
	case arg == f:
		return "", true, true
	case strings.HasPrefix(arg, f+"="):
		return arg[len(f)+1:], true, false
	case len(f) == 2 && f[1] != '-' && strings.HasPrefix(arg, f):
		return arg[2:], true, false
	}
	return "", false, false
}

// configKey is the key a key=value override sets, as the CLIs read it:
// each dotted part without the spaces or quotes around it.
func configKey(override string) string {
	key, _, _ := strings.Cut(override, "=")
	parts := strings.Split(key, ".")
	for i, part := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(part), `"'`)
	}
	return strings.Join(parts, ".")
}

// Split is s's words as a POSIX shell splits them, expanding nothing:
// quotes and backslashes as sh has them, and ” an empty word.
func Split(s string) ([]string, error) {
	var words []string
	var w strings.Builder
	word := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case ' ', '\t', '\n':
			if word {
				words = append(words, w.String())
				w.Reset()
				word = false
			}
		case '\\':
			i++
			if i == len(s) {
				return nil, errors.New("a \\ at the end escapes nothing")
			}
			if s[i] != '\n' {
				w.WriteByte(s[i])
				word = true
			}
		case '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, errors.New("a ' is left open")
			}
			w.WriteString(s[i+1 : i+1+j])
			i += j + 1
			word = true
		case '"':
			word = true
			for i++; ; i++ {
				if i == len(s) {
					return nil, errors.New(`a " is left open`)
				}
				if s[i] == '"' {
					break
				}
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\\n", s[i+1]) >= 0 {
					i++
					if s[i] == '\n' {
						continue
					}
				}
				w.WriteByte(s[i])
			}
		default:
			w.WriteByte(c)
			word = true
		}
	}
	if word {
		words = append(words, w.String())
	}
	return words, nil
}

// Flags are the flags a Placement reserves, as Settings › Providers lists
// them.
func (p Placement) Flags() []string {
	var out []string
	for _, r := range p.Reserved {
		for _, f := range r.Flags {
			if len(r.Keys) == 0 {
				out = append(out, f)
				continue
			}
			for _, k := range r.Keys {
				out = append(out, f+" "+k)
			}
		}
	}
	return out
}

// Environ is env with what prog sets in place of what env had of it.
func (prog Program) Environ(env []string) []string {
	for _, kv := range prog.Env {
		k, _, _ := strings.Cut(kv, "=")
		env = slices.DeleteFunc(env, func(e string) bool { return strings.HasPrefix(e, k+"=") })
	}
	return append(env, prog.Env...)
}

// Getenv is the value of k in prog's environment: its own, else the
// engine's.
func (prog Program) Getenv(k string) string {
	for _, kv := range slices.Backward(prog.Env) {
		if v, ok := strings.CutPrefix(kv, k+"="); ok {
			return v
		}
	}
	return os.Getenv(k)
}

// Placed is an Adapter whose program a Launch places.
type Placed interface {
	Adapter
	Placement() Placement
	// Place has every launch from now on start as l does; what runs
	// keeps how it started.
	Place(l Launch)
	// Version is what prog says of itself to --version.
	Version(ctx context.Context, prog Program) (string, error)
	// Command is how prog starts for a new thread, less the flags each
	// thread adds of its own.
	Command(prog Program) []string
}

// Unplaced is err from Placement.Program as Describe and Open return it:
// how to install the program when none was found, else what to fix of
// the Launch that names it, and how to install it when its path is
// what's wrong.
func Unplaced(err error, name, install string) error {
	var bad *LaunchError
	switch {
	case errors.Is(err, ErrNoProgram):
		return Needs{Kind: ErrNotInstalled, Help: install}
	case errors.As(err, &bad):
		help := name + " can't start as Settings › Providers sets it up: " + bad.Error() + "."
		if bad.Path != "" {
			help += "\n" + install
		}
		return Needs{Kind: ErrNotInstalled, Help: help}
	}
	return err
}

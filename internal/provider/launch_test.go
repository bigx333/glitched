package provider_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"glitched/internal/provider"
)

var placement = provider.Placement{
	Names:    []string{"demo-cli"},
	HomeVar:  "DEMO_HOME",
	Reserved: []provider.Reserved{{Flags: []string{"--model", "-m"}}, {Flags: []string{"-c", "--config"}, Keys: []string{"sandbox_mode"}}},
}

func program(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestALaunchIsCheckedSettingBySetting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	spaced := filepath.Join(home, "my tools")
	os.MkdirAll(spaced, 0o755)
	bin := program(t, spaced, "demo cli")
	folder := filepath.Join(home, "demo folder")
	os.MkdirAll(folder, 0o755)
	notExec := filepath.Join(home, "plain")
	os.WriteFile(notExec, nil, 0o644)

	prog, err := placement.Program(provider.Launch{Path: "~/my tools/demo cli", Home: "~/demo folder", Args: `--name "a b" -c features.x=true --verbose`})
	if err != nil {
		t.Fatal(err)
	}
	if prog.Path != bin || !slices.Equal(prog.Env, []string{"DEMO_HOME=" + folder}) || !slices.Equal(prog.Args, []string{"--name", "a b", "-c", "features.x=true", "--verbose"}) {
		t.Errorf("program = %+v", prog)
	}

	for _, c := range []struct {
		l          provider.Launch
		path, home string
		args       string
	}{
		{l: provider.Launch{Path: "bin/demo"}, path: "is relative"},
		{l: provider.Launch{Path: "demo-cli"}, path: "is relative"},
		{l: provider.Launch{Path: filepath.Join(home, "none")}, path: "there is no file at"},
		{l: provider.Launch{Path: spaced}, path: "is a folder"},
		{l: provider.Launch{Path: notExec}, path: "isn't executable"},
		{l: provider.Launch{Path: bin, Home: "rel"}, home: "is relative"},
		{l: provider.Launch{Path: bin, Home: filepath.Join(home, "missing")}, home: "there is no folder at"},
		{l: provider.Launch{Path: bin, Home: bin}, home: "is a file"},
		{l: provider.Launch{Path: bin, Args: `--name "open`}, args: "quoting"},
		{l: provider.Launch{Path: bin, Args: "--model x"}, args: "--model is set by Glitched itself"},
		{l: provider.Launch{Path: bin, Args: "-m=x"}, args: "-m is set by Glitched itself"},
		{l: provider.Launch{Path: bin, Args: "-c sandbox_mode=danger-full-access"}, args: "-c sandbox_mode"},
		{l: provider.Launch{Path: bin, Args: "--config=sandbox_mode.x=1"}, args: "--config sandbox_mode"},
		{l: provider.Launch{Path: bin, Args: "-csandbox_mode=danger-full-access"}, args: "-c sandbox_mode"},
		{l: provider.Launch{Path: bin, Args: `-c " sandbox_mode =danger-full-access"`}, args: "-c sandbox_mode"},
		{l: provider.Launch{Path: bin, Args: `--config '"sandbox_mode".writable_roots=[]'`}, args: "--config sandbox_mode"},
		{l: provider.Launch{Path: bin, Args: "-mx"}, args: "-m is set by Glitched itself"},
		{l: provider.Launch{Path: bin, Args: "--verbose --"}, args: "-- would make"},
		{l: provider.Launch{Path: "rel", Home: "rel", Args: "--model"}, path: "relative", home: "relative", args: "--model"},
	} {
		_, err := placement.Program(c.l)
		var bad *provider.LaunchError
		if !errors.As(err, &bad) {
			t.Errorf("%+v: %v, want a LaunchError", c.l, err)
			continue
		}
		for _, f := range []struct{ got, want string }{{bad.Path, c.path}, {bad.Home, c.home}, {bad.Args, c.args}} {
			if f.want == "" && f.got != "" || !strings.Contains(f.got, f.want) {
				t.Errorf("%+v: %+v, want %q in each of path, home, args %q %q %q", c.l, bad, f.want, c.path, c.home, c.args)
			}
		}
	}
}

func TestAnEmptyPathFindsTheProgram(t *testing.T) {
	home, onPath := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", onPath)
	if _, err := placement.Program(provider.Launch{}); !errors.Is(err, provider.ErrNoProgram) {
		t.Errorf("with none installed: %v, want ErrNoProgram", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	local := program(t, filepath.Join(home, ".local", "bin"), "demo-cli")
	if prog, err := placement.Program(provider.Launch{}); err != nil || prog.Path != local {
		t.Errorf("in ~/.local/bin: %+v, %v", prog, err)
	}
	found := program(t, onPath, "demo-cli")
	if prog, err := placement.Program(provider.Launch{}); err != nil || prog.Path != found {
		t.Errorf("on PATH: %+v, %v, want %s", prog, err, found)
	}
}

func TestAProgramsHomeReplacesTheEngines(t *testing.T) {
	prog := provider.Program{Env: []string{"DEMO_HOME=/b"}}
	got := prog.Environ([]string{"A=1", "DEMO_HOME=/a", "DEMO_HOMEX=2"})
	if !slices.Equal(got, []string{"A=1", "DEMO_HOMEX=2", "DEMO_HOME=/b"}) {
		t.Errorf("environ = %q", got)
	}
}

func TestAProgramWithNoFolderTakesNoHome(t *testing.T) {
	dir := t.TempDir()
	p := provider.Placement{Names: []string{"x"}}
	_, err := p.Program(provider.Launch{Path: program(t, dir, "x"), Home: dir})
	var bad *provider.LaunchError
	if !errors.As(err, &bad) || bad.Home == "" {
		t.Errorf("a Home for a program with no HomeVar: %v", err)
	}
}

func TestArgumentsSplitAsAShellSplitsThem(t *testing.T) {
	for in, want := range map[string][]string{
		"":                  nil,
		"  -a   b\t":        {"-a", "b"},
		"--name '' --x":     {"--name", "", "--x"},
		`--name "a b" c\ d`: {"--name", "a b", "c d"},
		`"it's" 'say "hi"'`: {"it's", `say "hi"`},
		`"a\"b\\c\$d\e"`:    {`a"b\c$d\e`},
		`--trail\ `:         {"--trail "},
		"a\\\nb":            {"ab"},
		`x'y'"z"`:           {"xyz"},
	} {
		got, err := provider.Split(in)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("Split(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{`"open`, `'open`, `end\`} {
		if _, err := provider.Split(in); err == nil {
			t.Errorf("Split(%q) has no error", in)
		}
	}
}

func TestAProgramNamedFromHomeIsFound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	p := provider.Placement{Names: []string{"~/bin/demo-cli"}}
	if _, err := p.Program(provider.Launch{}); !errors.Is(err, provider.ErrNoProgram) {
		t.Errorf("with none there: %v, want ErrNoProgram", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := program(t, filepath.Join(home, "bin"), "demo-cli")
	if prog, err := p.Program(provider.Launch{}); err != nil || prog.Path != want {
		t.Errorf("in ~/bin: %+v, %v, want %s", prog, err, want)
	}
}

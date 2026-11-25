package acp

import (
	"testing"
)

func TestCustomProfileToProfile(t *testing.T) {
	p, err := (CustomProfile{
		Provider: "my-acp",
		Name:     "My ACP",
		Binary:   "/usr/local/bin/my-acp",
		Args:     []string{"--acp"},
		Env:      map[string]string{"FOO": "bar"},
	}).ToProfile()
	if err != nil {
		t.Fatal(err)
	}
	if p.Provider != "my-acp" || p.Name != "My ACP" || p.Binaries[0] != "/usr/local/bin/my-acp" || p.Env["FOO"] != "bar" || p.Env["BROWSER"] != "true" {
		t.Errorf("unexpected profile: %+v", p)
	}

	// Empty name & args get clean defaults.
	def, err := (CustomProfile{Provider: "tool2", Binary: "tool2"}).ToProfile()
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "tool2" || len(def.Args) != 1 || def.Args[0] != "acp" {
		t.Errorf("defaulted profile = %+v", def)
	}

	for _, bad := range []CustomProfile{
		{Provider: "", Binary: "bin"},
		{Provider: "BadCaps", Binary: "bin"},
		{Provider: "claude", Binary: "bin"},
		{Provider: "gemini", Binary: "bin"},
		{Provider: "good-id", Binary: ""},
	} {
		if _, err := bad.ToProfile(); err == nil {
			t.Errorf("expected error for %+v", bad)
		}
	}
}

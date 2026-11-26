package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDoctorOnFreshDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	var err error
	out := stdout(t, func() {
		err = doctor(context.Background(), []string{"-dir", dir})
	})
	if err != nil {
		t.Fatalf("doctor on fresh dir: %v\n%s", err, out)
	}
	for _, want := range []string{"dir", "config", "engine", "engine.db"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func TestDoctorWithRunningEngineAndJSON(t *testing.T) {
	dir := stateDir(t)
	stop := serving(t, dir)
	defer stop()

	var err error
	out := stdout(t, func() {
		err = doctor(context.Background(), []string{"-dir", dir, "-json"})
	})
	if err != nil {
		t.Fatalf("doctor -json: %v\n%s", err, out)
	}
	var report doctorReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("unmarshal doctor JSON: %v\n%s", err, out)
	}
	if report.Dir != dir {
		t.Errorf("report.Dir = %q, want %q", report.Dir, dir)
	}
	statusOf := func(name string) (doctorCheck, bool) {
		i := slices.IndexFunc(report.Checks, func(c doctorCheck) bool { return c.Name == name })
		if i < 0 {
			return doctorCheck{}, false
		}
		return report.Checks[i], true
	}
	for _, name := range []string{"dir", "config", "engine", "engine.db", "hub.db"} {
		c, ok := statusOf(name)
		if !ok {
			t.Errorf("missing check %q in JSON report", name)
			continue
		}
		if c.Status != "ok" {
			t.Errorf("check %q = %+v, want status ok", name, c)
		}
	}
}

func TestDoctorDetectsCorruptConfig(t *testing.T) {
	dir := stateDir(t)
	if err := os.WriteFile(filepath.Join(dir, "engine.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	out := stdout(t, func() {
		err = doctor(context.Background(), []string{"-dir", dir})
	})
	if err == nil {
		t.Fatalf("doctor succeeded on corrupt engine.json:\n%s", out)
	}
	if !strings.Contains(out, "[fail] config") {
		t.Errorf("expected [fail] config in output:\n%s", out)
	}
}

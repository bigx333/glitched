package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"glitched/internal/service"
)

func TestServiceCLIStatusAndUsage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	dir := filepath.Join(home, "state")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = oldOut })

	if err := serviceCmd(context.Background(), []string{"status", "-json", "-dir", dir}); err != nil {
		w.Close()
		t.Fatal(err)
	}
	w.Close()
	os.Stdout = oldOut

	var st service.Status
	if err := json.NewDecoder(r).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Installed || st.Running {
		t.Errorf("unexpected status for fresh home: %+v", st)
	}

	if err := serviceCmd(context.Background(), nil); !errors.As(err, new(usageError)) {
		t.Errorf("no args error = %v, want usageError", err)
	}
	if err := serviceCmd(context.Background(), []string{"bogus"}); !errors.As(err, new(usageError)) {
		t.Errorf("bad action error = %v, want usageError", err)
	} else if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error = %v, want action name", err)
	}
}

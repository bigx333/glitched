package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/tailnet"
	"glitched/internal/transport"
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warn, fail
	Detail string `json:"detail"`
}

type doctorReport struct {
	Dir    string        `json:"dir"`
	Checks []doctorCheck `json:"checks"`
}

func doctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	dir := dirFlag(fs)
	asJSON := fs.Bool("json", false, "output JSON report")
	fs.Parse(args)

	report := runDoctor(ctx, *dir)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	} else {
		for _, c := range report.Checks {
			fmt.Printf("[%-4s] %-12s %s\n", c.Status, c.Name, c.Detail)
		}
	}
	for _, c := range report.Checks {
		if c.Status == "fail" {
			return fmt.Errorf("doctor: %s failed: %s", c.Name, c.Detail)
		}
	}
	return nil
}

func runDoctor(ctx context.Context, dir string) doctorReport {
	var checks []doctorCheck
	checks = append(checks, checkDir(dir))

	cfg, cfgCheck := checkConfig(dir)
	checks = append(checks, cfgCheck)

	checks = append(checks, checkEngineSocket(ctx, dir))
	checks = append(checks, checkDB(ctx, dir, "engine.db"))
	if _, err := os.Stat(filepath.Join(dir, "hub.db")); err == nil {
		checks = append(checks, checkDB(ctx, dir, "hub.db"))
	}
	checks = append(checks, checkTailscale(ctx))

	for _, p := range []struct {
		name string
		bin  string
	}{
		{"claude", "claude"},
		{"codex", "codex"},
		{"antigravity", "agy"},
	} {
		custom := ""
		if cfg != nil {
			custom = cfg.Launch[p.name].Path
		}
		checks = append(checks, checkProviderCLI(ctx, p.name, p.bin, custom))
	}

	return doctorReport{Dir: dir, Checks: checks}
}

func checkDir(dir string) doctorCheck {
	fi, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return doctorCheck{Name: "dir", Status: "warn", Detail: fmt.Sprintf("%s does not exist yet (created on first start)", dir)}
	}
	if err != nil {
		return doctorCheck{Name: "dir", Status: "fail", Detail: err.Error()}
	}
	if !fi.IsDir() {
		return doctorCheck{Name: "dir", Status: "fail", Detail: fmt.Sprintf("%s is not a directory", dir)}
	}
	if runtime.GOOS != "windows" {
		if perm := fi.Mode().Perm(); perm != 0o700 {
			return doctorCheck{Name: "dir", Status: "warn", Detail: fmt.Sprintf("%s has mode %04o, want 0700", dir, perm)}
		}
	}
	return doctorCheck{Name: "dir", Status: "ok", Detail: dir}
}

func checkConfig(dir string) (*daemon.Config, doctorCheck) {
	cfg, err := daemon.Defaults(dir)
	if err != nil {
		return nil, doctorCheck{Name: "config", Status: "fail", Detail: err.Error()}
	}
	return &cfg, doctorCheck{Name: "config", Status: "ok", Detail: fmt.Sprintf("hub=%v tailnet=%v", cfg.Hub, cfg.Tailnet)}
}

func checkEngineSocket(ctx context.Context, dir string) doctorCheck {
	probeCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	sock := daemon.Socket(dir)
	c := transport.NewEndpointClient(sock, api.ID{})
	info, err := c.Info(probeCtx)
	c.Close()
	if err == nil {
		return doctorCheck{
			Name:   "engine",
			Status: "ok",
			Detail: fmt.Sprintf("serving as %s (host %s, protocol %d)", info.Name, info.ID, info.Protocol),
		}
	}
	if _, statErr := os.Stat(sock.Address); statErr == nil {
		return doctorCheck{
			Name:   "engine",
			Status: "warn",
			Detail: fmt.Sprintf("%s exists but engine did not answer (%v)", sock.Address, err),
		}
	}
	return doctorCheck{Name: "engine", Status: "warn", Detail: "not running"}
}

const walWarnBytes = 64 << 20

func checkDB(ctx context.Context, dir, name string) doctorCheck {
	path := filepath.Join(dir, name)
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return doctorCheck{Name: name, Status: "warn", Detail: "not created yet"}
	}
	if err != nil {
		return doctorCheck{Name: name, Status: "fail", Detail: err.Error()}
	}
	var walSize int64
	if wfi, err := os.Stat(path + "-wal"); err == nil {
		walSize = wfi.Size()
	}

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_busy_timeout=1000")
	if err != nil {
		return doctorCheck{Name: name, Status: "fail", Detail: err.Error()}
	}
	defer db.Close()

	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return doctorCheck{Name: name, Status: "fail", Detail: fmt.Sprintf("read user_version: %v", err)}
	}
	var check string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&check); err != nil {
		return doctorCheck{Name: name, Status: "fail", Detail: fmt.Sprintf("quick_check: %v", err)}
	}
	if check != "ok" {
		return doctorCheck{Name: name, Status: "fail", Detail: fmt.Sprintf("quick_check=%q", check)}
	}
	detail := fmt.Sprintf("schema v%d, db %s, wal %s", version, humanBytes(fi.Size()), humanBytes(walSize))
	if walSize > walWarnBytes {
		return doctorCheck{Name: name, Status: "warn", Detail: detail + " (large WAL)"}
	}
	return doctorCheck{Name: name, Status: "ok", Detail: detail}
}

func checkTailscale(ctx context.Context) doctorCheck {
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	tn, err := tailnet.Detect(probeCtx)
	if err != nil {
		return doctorCheck{Name: "tailscale", Status: "warn", Detail: err.Error()}
	}
	if !tn.Running() {
		return doctorCheck{Name: "tailscale", Status: "warn", Detail: fmt.Sprintf("installed (%s)", strings.ToLower(tn.State))}
	}
	return doctorCheck{Name: "tailscale", Status: "ok", Detail: tailnetState(tn)}
}

func checkProviderCLI(ctx context.Context, name, defaultBin, customPath string) doctorCheck {
	checkName := "cli:" + name
	path := findProviderBinary(defaultBin, customPath)
	if path == "" {
		return doctorCheck{Name: checkName, Status: "warn", Detail: "not found"}
	}
	vCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(vCtx, path, "--version").CombinedOutput()
	ver := strings.TrimSpace(string(out))
	if i := strings.IndexByte(ver, '\n'); i >= 0 {
		ver = strings.TrimSpace(ver[:i])
	}
	if err != nil {
		return doctorCheck{Name: checkName, Status: "warn", Detail: fmt.Sprintf("%s (--version failed: %v)", path, err)}
	}
	if ver == "" {
		return doctorCheck{Name: checkName, Status: "ok", Detail: path}
	}
	return doctorCheck{Name: checkName, Status: "ok", Detail: fmt.Sprintf("%s (%s)", path, ver)}
}

func findProviderBinary(defaultBin, customPath string) string {
	if customPath != "" {
		if p, err := exec.LookPath(customPath); err == nil {
			return p
		}
		if fi, err := os.Stat(customPath); err == nil && !fi.IsDir() {
			return customPath
		}
		return ""
	}
	if p, err := exec.LookPath(defaultBin); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		cand := filepath.Join(home, ".local", "bin", defaultBin)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	return ""
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

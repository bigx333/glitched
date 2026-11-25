package service

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// launchd runs the service as a user agent in the GUI domain.
type launchd struct{}

func (launchd) path(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

func (launchd) render(opts Options) ([]byte, error) { return RenderPlist(opts) }

func (launchd) parse(data []byte) (binary, dir, log string) {
	var doc struct {
		Dict struct {
			Items []struct {
				XMLName xml.Name
				Text    string   `xml:",chardata"`
				Strings []string `xml:"string"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if xml.Unmarshal(data, &doc) != nil {
		return "", "", ""
	}
	key := ""
	for _, it := range doc.Dict.Items {
		switch {
		case it.XMLName.Local == "key":
			key = it.Text
			continue
		case key == "ProgramArguments":
			binary, dir = serveOf(it.Strings)
		case key == "StandardOutPath":
			log = it.Text
		}
		key = ""
	}
	return binary, dir, log
}

// serveOf reads the binary and -dir of a serve command line.
func serveOf(args []string) (binary, dir string) {
	if len(args) == 0 {
		return "", ""
	}
	for i, a := range args[:len(args)-1] {
		if a == "-dir" || a == "--dir" {
			dir = args[i+1]
		}
	}
	return args[0], dir
}

func domainTarget() string  { return fmt.Sprintf("gui/%d", os.Getuid()) }
func serviceTarget() string { return domainTarget() + "/" + Label }

func (launchd) loaded(ctx context.Context) bool {
	_, err := command(ctx, "launchctl", "print", serviceTarget())
	return err == nil
}

func (l launchd) load(ctx context.Context, path string, restart bool) error {
	loaded := l.loaded(ctx)
	switch {
	case loaded && !restart:
		// Starts the job if it stopped; one that runs keeps running.
		_, err := command(ctx, "launchctl", "kickstart", serviceTarget())
		return err
	case loaded:
		if _, err := command(ctx, "launchctl", "bootout", serviceTarget()); err != nil {
			return err
		}
	}
	// launchd can refuse a bootstrap for a moment after a bootout, while the
	// old job winds down.
	var err error
	for range 10 {
		if _, err = command(ctx, "launchctl", "bootstrap", domainTarget(), path); err == nil || !loaded {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(300 * time.Millisecond):
		}
	}
	return err
}

func (l launchd) unload(ctx context.Context) error {
	if !l.loaded(ctx) {
		return nil
	}
	_, err := command(ctx, "launchctl", "bootout", serviceTarget())
	return err
}

func (launchd) state(ctx context.Context) (running bool, pid int) {
	out, err := command(ctx, "launchctl", "print", serviceTarget())
	if err != nil {
		return false, 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		t := strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(t, "pid = "); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				pid, running = n, true
			}
		}
		if t == "state = running" {
			running = true
		}
	}
	return running, pid
}

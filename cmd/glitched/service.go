package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"

	"glitched/internal/service"
)

func serviceCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("usage: glitched service <install|uninstall|status> [-dir DIR] [-binary PATH] [-json]")
	}
	var action string
	var flagArgs []string
	if slices.Contains([]string{"install", "uninstall", "status"}, args[0]) {
		action = args[0]
		flagArgs = args[1:]
	} else {
		flagArgs = args
	}

	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	dir := dirFlag(fs)
	bin := fs.String("binary", "", "glitched binary for the service to run")
	jsonOut := fs.Bool("json", false, "output status as JSON")
	if err := fs.Parse(flagArgs); err != nil {
		return usageError(err.Error())
	}
	if action == "" {
		if fs.NArg() == 0 {
			return usageError("usage: glitched service <install|uninstall|status> [-dir DIR] [-binary PATH] [-json]")
		}
		action = fs.Arg(0)
		if fs.NArg() > 1 {
			return usageError("service: unexpected arguments")
		}
	} else if fs.NArg() > 0 {
		return usageError("service: unexpected arguments")
	}

	opts := service.Options{Binary: *bin, Dir: *dir}
	var st service.Status
	var err error
	switch action {
	case "install":
		st, err = service.Install(ctx, opts)
	case "uninstall":
		st, err = service.Uninstall(ctx, opts)
	case "status":
		st = service.Inspect(ctx, opts)
	default:
		return usageError(fmt.Sprintf("service: unknown action %q (want install, uninstall, or status)", action))
	}
	if err != nil {
		return err
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	switch {
	case !st.Supported:
		fmt.Printf("supported  no (%s)\n", st.Detail)
		return nil
	case !st.Installed:
		fmt.Printf("installed  no (%s)\n", st.UnitPath)
		return nil
	case st.Foreign:
		fmt.Printf("installed  for another directory, %s, not %s; manage it with -dir %s\n", st.Dir, *dir, st.Dir)
	}
	fmt.Printf("status     %s\nunit       %s\n", health(st), st.UnitPath)
	if st.Dir != "" {
		fmt.Printf("dir        %s\n", st.Dir)
	}
	if st.LogPath != "" {
		fmt.Printf("log        %s\n", st.LogPath)
	}
	return nil
}

// health says whether an installed service runs.
func health(st service.Status) string {
	switch {
	case st.Running && st.PID > 0:
		return fmt.Sprintf("running (pid %d)", st.PID)
	case st.Running:
		return "running"
	case st.Starting && st.LastExit != "":
		return "restarting, last exited " + st.LastExit
	case st.Starting:
		return "starting"
	case st.LastExit != "":
		return "installed (stopped), last exited " + st.LastExit
	}
	return "installed (stopped)"
}

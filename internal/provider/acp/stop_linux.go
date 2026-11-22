package acp

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

func processes() []process {
	entries, _ := os.ReadDir("/proc")
	var out []process
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// The command, in parentheses, may hold spaces: the fields after
		// it start past its last ")".
		i := bytes.LastIndexByte(stat, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(stat[i+1:]))
		if len(f) < 20 {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		pgid, _ := strconv.Atoi(f[2])
		start, _ := strconv.ParseInt(f[19], 10, 64)
		out = append(out, process{pid: pid, ppid: ppid, pgid: pgid, start: start, zombie: f[0] == "Z"})
	}
	return out
}

func carries(pid int, prefix string) bool {
	env, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return false
	}
	for kv := range bytes.SplitSeq(env, []byte{0}) {
		if marks(string(kv), prefix) {
			return true
		}
	}
	return false
}

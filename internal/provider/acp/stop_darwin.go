package acp

import (
	"bytes"
	"encoding/binary"
	"os"

	"golang.org/x/sys/unix"
)

// szomb is SZOMB, a zombie's p_stat in <sys/proc.h>.
const szomb = 5

func processes() []process {
	procs, _ := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	out := make([]process, 0, len(procs))
	for _, p := range procs {
		out = append(out, process{
			pid: int(p.Proc.P_pid), ppid: int(p.Eproc.Ppid), pgid: int(p.Eproc.Pgid),
			start:  p.Proc.P_starttime.Sec*1e6 + int64(p.Proc.P_starttime.Usec),
			zombie: p.Proc.P_stat == szomb,
		})
	}
	return out
}

// carries is whether pid's environment holds a mark under prefix. The
// kernel lays it out as argc, the executable's path, NUL padding, argc
// arguments, then the environment up to an empty string.
func carries(pid int, prefix string) bool {
	args, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(args) < 4 {
		return false
	}
	argc := int(binary.NativeEndian.Uint32(args))
	rest := args[4:]
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return false
	}
	rest = bytes.TrimLeft(rest[i:], "\x00")
	for range argc {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return false
		}
		rest = rest[i+1:]
	}
	for kv := range bytes.SplitSeq(rest, []byte{0}) {
		if len(kv) == 0 {
			return false
		}
		if marks(string(kv), prefix) {
			return true
		}
	}
	return false
}

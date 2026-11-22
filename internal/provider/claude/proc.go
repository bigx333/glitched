package claude

import (
	"bytes"
	"strings"

	"glitched/internal/api"
)

// procEnv names, in the environment of a claude, its mark. Every process
// claude starts inherits it, so the engine can find them all, tool shells
// in sessions of their own included, after the engine that started them is
// gone.
const procEnv = "GLITCHED_CLAUDE_PROCESS"

// mark names claude processes: a launch's mark names one claude and what
// it starts, a thread's names every launch of the thread. Ids are
// fixed-length, so a launch's mark matches no other launch, and a thread's
// is a prefix of each of its launches'.
type mark string

func launchMark(thread, launch api.ID) mark { return mark(thread.String() + "-" + launch.String()) }

func threadMark(thread api.ID) mark { return mark(thread.String() + "-") }

// covers is whether every process other names is one of m's.
func (m mark) covers(other mark) bool { return strings.HasPrefix(string(other), string(m)) }

// tags is whether kv, an entry of a process's environment, makes the
// process one of m's.
func (m mark) tags(kv []byte) bool {
	v, ok := bytes.CutPrefix(kv, []byte(procEnv+"="))
	return ok && m.covers(mark(v))
}

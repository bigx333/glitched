// Package luau links the official Luau compiler and VM, vendored in
// upstream/ by update.sh, and exposes one sandboxed state per VM. Building
// it needs cgo and a C++17 compiler.
//
// A VM opens only base, table, string, math, utf8, bit32 and buffer, with
// no print, loadstring, getfenv or setfenv, so a script has no file,
// network, environment or process access. Each call has a step budget, a
// wall-clock budget and a heap cap.
package luau

/*
#cgo CXXFLAGS: -std=c++17 -O2 -DNDEBUG
#cgo CXXFLAGS: -I${SRCDIR}/upstream/Common/include -I${SRCDIR}/upstream/Ast/include
#cgo CXXFLAGS: -I${SRCDIR}/upstream/Bytecode/include -I${SRCDIR}/upstream/Compiler/include
#cgo CXXFLAGS: -I${SRCDIR}/upstream/VM/include -I${SRCDIR}/upstream/VM/src
#cgo !darwin LDFLAGS: -lstdc++ -lm
#cgo darwin LDFLAGS: -lc++

#include <stdlib.h>
#include "shim.h"
*/
import "C"

import (
	"errors"
	"time"
	"unsafe"
)

const (
	// heapLimit caps what the Luau allocator hands out to one VM.
	heapLimit = 32 << 20
	// steps is how many safepoints (loop back edges and calls) one call
	// may pass.
	steps = 2_000_000
	// callTimeout bounds one call's wall-clock time, checked at safepoints.
	callTimeout = 250 * time.Millisecond
)

// ScriptError is an error the script raised or caused: a syntax error, a
// runtime error, running out of steps, time or memory. The VM stays usable.
type ScriptError struct{ Message string }

func (e *ScriptError) Error() string { return e.Message }

// VM is one Luau state. Its methods are not safe for concurrent use.
type VM struct{ vm *C.mg_vm }

// New starts a VM and runs prelude in it with the opened globals. prelude
// must return a table: Call looks its functions up by name. After prelude,
// every global table is frozen.
func New(prelude string) (*VM, error) {
	src := C.CString(prelude)
	defer C.free(unsafe.Pointer(src))
	lim := C.mg_limits{heap: heapLimit, steps: steps, micros: C.longlong(callTimeout.Microseconds())}
	var msg *C.char
	vm := C.mg_new(src, C.size_t(len(prelude)), lim, &msg)
	if vm == nil {
		defer C.mg_free_string(msg)
		return nil, errors.New("luau: prelude: " + C.GoString(msg))
	}
	return &VM{vm}, nil
}

// Call compiles chunk as a function in a fresh sandboxed thread and passes
// it to the prelude's function fn, which must return a string. name labels
// the chunk in error messages. A *ScriptError leaves the VM usable; any
// other error means it is closed.
func (v *VM) Call(fn, name, chunk string) (string, error) {
	if v.vm == nil {
		return "", errors.New("luau: VM is closed")
	}
	cfn, cname, csrc := C.CString(fn), C.CString("="+name), C.CString(chunk)
	defer C.free(unsafe.Pointer(cfn))
	defer C.free(unsafe.Pointer(cname))
	defer C.free(unsafe.Pointer(csrc))
	var out *C.char
	var n C.size_t
	status := C.mg_call(v.vm, cfn, cname, csrc, C.size_t(len(chunk)), &out, &n)
	s := C.GoStringN(out, C.int(n))
	C.mg_free_string(out)
	switch status {
	case 0:
		return s, nil
	case C.MG_BROKEN:
		v.Close()
		return "", errors.New(s)
	}
	return "", &ScriptError{s}
}

// Close releases the VM. It is safe to call more than once.
func (v *VM) Close() {
	if v.vm != nil {
		C.mg_close(v.vm)
		v.vm = nil
	}
}

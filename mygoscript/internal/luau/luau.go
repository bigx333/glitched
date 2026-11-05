// Package luau runs the Luau VM, compiled to WebAssembly by build.sh, inside
// wazero. A VM gets no files, no network, no clock beyond WASI's and no
// process access; its memory and the work of each call are capped.
package luau

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed luau.wasm
var wasm []byte

const (
	// wasmPages caps the module's linear memory at 64 MiB.
	wasmPages = 1024
	// heapLimit caps what the Luau allocator hands out, below wasmPages,
	// so running out is a script error rather than a trap.
	heapLimit = 32 << 20
	// steps is how many safepoints (loop back edges and calls) one call
	// may pass.
	steps = 2_000_000
	// callTimeout stops a call that the step budget did not.
	callTimeout = 2 * time.Second
)

// ScriptError is an error the script raised or caused: a syntax error, a
// runtime error, running out of steps or memory. The VM stays usable.
type ScriptError struct{ Message string }

func (e *ScriptError) Error() string { return e.Message }

var (
	once     sync.Once
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	setupErr error
)

func setup() {
	ctx := context.Background()
	cfg := wazero.NewRuntimeConfig().
		WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling).
		WithMemoryLimitPages(wasmPages).
		WithMemoryCapacityFromMax(true).
		WithCloseOnContextDone(true)
	runtime = wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, setupErr = wasi_snapshot_preview1.Instantiate(ctx, runtime); setupErr != nil {
		return
	}
	compiled, setupErr = runtime.CompileModule(ctx, wasm)
}

// VM is one Luau state. Its methods are not safe for concurrent use.
type VM struct {
	mod                                        api.Module
	alloc, free, boot, call, result, resultLen api.Function
}

// New starts a VM and runs prelude in it with the full standard globals.
// prelude must return a table: Call looks its functions up by name. After
// prelude, every global table is frozen.
func New(prelude string) (*VM, error) {
	once.Do(setup)
	if setupErr != nil {
		return nil, setupErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	// No WithFS, WithStdout, WithArgs or WithEnv: the module sees no
	// files, discards what it prints and reads no environment.
	mod, err := runtime.InstantiateModule(ctx, compiled,
		wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize"))
	if err != nil {
		return nil, err
	}
	vm := &VM{
		mod:       mod,
		alloc:     mod.ExportedFunction("mg_alloc"),
		free:      mod.ExportedFunction("mg_free"),
		boot:      mod.ExportedFunction("mg_boot"),
		call:      mod.ExportedFunction("mg_call"),
		result:    mod.ExportedFunction("mg_result"),
		resultLen: mod.ExportedFunction("mg_result_len"),
	}
	src, err := vm.write(ctx, prelude)
	if err == nil {
		defer vm.release(ctx, src)
		_, err = vm.finish(ctx, vm.boot, src, uint64(len(prelude)), heapLimit, steps)
	}
	if err != nil {
		mod.Close(ctx)
		return nil, fmt.Errorf("luau: prelude: %w", err)
	}
	return vm, nil
}

// Call compiles chunk as a function in a fresh sandboxed thread and passes
// it to the prelude's function fn, which must return a string. name labels
// the chunk in error messages. A *ScriptError leaves the VM usable; any
// other error means it is closed.
func (vm *VM) Call(fn, name, chunk string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var ptrs [3]uint64
	for i, s := range []string{fn, "=" + name, chunk} {
		p, err := vm.write(ctx, s)
		if err != nil {
			return "", vm.fail(ctx, err)
		}
		defer vm.release(ctx, p)
		ptrs[i] = p
	}
	return vm.finish(ctx, vm.call, ptrs[0], uint64(len(fn)), ptrs[1], ptrs[2], uint64(len(chunk)), steps)
}

// Close releases the VM.
func (vm *VM) Close() error { return vm.mod.Close(context.Background()) }

func (vm *VM) finish(ctx context.Context, f api.Function, params ...uint64) (string, error) {
	res, err := f.Call(ctx, params...)
	if err != nil {
		return "", vm.fail(ctx, err)
	}
	out, err := vm.readResult(ctx)
	if err != nil {
		return "", vm.fail(ctx, err)
	}
	if int32(res[0]) != 0 {
		return "", &ScriptError{out}
	}
	return out, nil
}

// fail closes the module after a trap or timeout: its memory may be
// mid-update, so it is not run again.
func (vm *VM) fail(ctx context.Context, err error) error {
	vm.mod.Close(ctx)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("luau: script ran longer than %s", callTimeout)
	}
	return fmt.Errorf("luau: %w", err)
}

func (vm *VM) readResult(ctx context.Context) (string, error) {
	p, err := vm.result.Call(ctx)
	if err != nil {
		return "", err
	}
	n, err := vm.resultLen.Call(ctx)
	if err != nil {
		return "", err
	}
	if n[0] == 0 {
		return "", nil
	}
	b, ok := vm.mod.Memory().Read(uint32(p[0]), uint32(n[0]))
	if !ok {
		return "", errors.New("result out of range")
	}
	return string(b), nil
}

// write copies s, NUL-terminated, into the module's memory.
func (vm *VM) write(ctx context.Context, s string) (uint64, error) {
	res, err := vm.alloc.Call(ctx, uint64(len(s)+1))
	if err != nil {
		return 0, err
	}
	p := uint32(res[0])
	if p == 0 || !vm.mod.Memory().WriteString(p, s) || !vm.mod.Memory().WriteByte(p+uint32(len(s)), 0) {
		return 0, errors.New("out of memory")
	}
	return uint64(p), nil
}

func (vm *VM) release(ctx context.Context, p uint64) { vm.free.Call(ctx, p) }

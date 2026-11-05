#!/usr/bin/env bash
# Rebuilds luau.wasm: the Luau VM and compiler plus shim.cpp, built for WASI.
# Needs curl, git and tar. Downloads into a scratch directory, so the repo
# only ever holds this script, the shim and the output.
set -euo pipefail

LUAU_VERSION=0.745
WASI_SDK_VERSION=34
here=$(cd "$(dirname "$0")" && pwd)
out=$here/../luau.wasm
work=${WORK:-$(mktemp -d)}

case "$(uname -s)-$(uname -m)" in
Linux-x86_64) host=x86_64-linux ;;
Linux-aarch64) host=arm64-linux ;;
Darwin-arm64) host=arm64-macos ;;
Darwin-x86_64) host=x86_64-macos ;;
*) echo "unsupported host $(uname -sm)" >&2; exit 1 ;;
esac

sdk=$work/wasi-sdk-$WASI_SDK_VERSION.0-$host
if [ ! -d "$sdk" ]; then
	curl -fsSL "https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-$WASI_SDK_VERSION/wasi-sdk-$WASI_SDK_VERSION.0-$host.tar.gz" | tar xz -C "$work"
fi
if [ ! -d "$work/luau" ]; then
	git -c advice.detachedHead=false clone -q --depth 1 -b "$LUAU_VERSION" https://github.com/luau-lang/luau "$work/luau"
fi

luau=$work/luau
# wazero implements the exnref exception proposal, not the legacy one.
flags=(--target=wasm32-wasip1 -std=c++17 -Oz -fwasm-exceptions -mllvm -wasm-use-legacy-eh=false
	-D_WASI_EMULATED_PROCESS_CLOCKS -Wno-deprecated-declarations
	-DLUA_API='extern "C"' -DLUACODE_API='extern "C"'
	-I"$luau/Common/include" -I"$luau/Ast/include" -I"$luau/Bytecode/include"
	-I"$luau/Compiler/include" -I"$luau/VM/include" -I"$luau/VM/src")

objs=()
mkdir -p "$work/obj"
for src in "$luau"/Common/src/*.cpp "$luau"/Ast/src/*.cpp "$luau"/Bytecode/src/*.cpp "$luau"/Compiler/src/*.cpp "$luau"/VM/src/*.cpp "$here/shim.cpp"; do
	obj=$work/obj/$(basename "$(dirname "$(dirname "$src")")")-$(basename "$src" .cpp).o
	"$sdk/bin/clang++" "${flags[@]}" -c "$src" -o "$obj" &
	objs+=("$obj")
	while [ "$(jobs -rp | wc -l)" -ge 8 ]; do wait -n; done
done
wait

"$sdk/bin/clang++" --target=wasm32-wasip1 -fwasm-exceptions -mexec-model=reactor -Oz -s \
	-Wl,--no-entry -Wl,--strip-all -lunwind -lwasi-emulated-process-clocks \
	"${objs[@]}" -o "$out"
echo "wrote $out ($(wc -c <"$out") bytes) from Luau $LUAU_VERSION, wasi-sdk $WASI_SDK_VERSION"

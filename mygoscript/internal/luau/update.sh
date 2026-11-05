#!/usr/bin/env bash
# Vendors the official Luau compiler and VM at LUAU_VERSION into upstream/
# and writes one cgo wrapper per source file. cgo only builds C++ files in
# the package directory, and Luau's VM sources do not build as one unit.
set -euo pipefail

LUAU_VERSION=${1:-0.745}
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

git -c advice.detachedHead=false clone -q --depth 1 -b "$LUAU_VERSION" https://github.com/luau-lang/luau "$work/luau"

rm -rf "$here/upstream" "$here"/luau_*.cpp
mkdir -p "$here/upstream"
for d in Common Ast Bytecode Compiler VM; do
	mkdir -p "$here/upstream/$d"
	cp -R "$work/luau/$d/include" "$here/upstream/$d/"
	cp -R "$work/luau/$d/src" "$here/upstream/$d/"
	for src in "$work/luau/$d/src"/*.cpp; do
		name=$(basename "$src")
		printf '#include "upstream/%s/src/%s"\n' "$d" "$name" >"$here/luau_${d}_$name"
	done
done
cp "$work/luau/LICENSE.txt" "$work/luau/lua_LICENSE.txt" "$here/upstream/"
echo "$LUAU_VERSION" >"$here/upstream/VERSION"
echo "vendored Luau $LUAU_VERSION: $(ls "$here"/luau_*.cpp | wc -l) sources"

#!/usr/bin/env bash
#
# Build the Go tools and inject the binaries into the plugins that use them.
#
# POSIX counterpart of build-tools.ps1, for macOS and Linux. Plugin bin/
# directories are on the Claude Code PATH while the plugin is enabled, so a
# skill can call the tool as a bare command.
#
# Usage:
#   ./scripts/build-tools.sh          build for this machine
#   ./scripts/build-tools.sh --all    cross-compile the matrix into tools/dist/

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tools_dir="$repo_root/tools"

# Each tool, and the plugin bin/ directories that should receive it.
tool_targets() {
    case "$1" in
        errorlog-parse) echo "$repo_root/plugins/sqlserver-toolkit/bin" ;;
        sqlq)           echo "$repo_root/plugins/sqlserver-toolkit/bin" ;;
        *) return 1 ;;
    esac
}

TOOLS=(errorlog-parse sqlq)

build_all=false
if [ "${1:-}" = "--all" ]; then
    build_all=true
fi

command -v go >/dev/null 2>&1 || {
    echo "error: the Go toolchain is required but was not found on PATH." >&2
    echo "       Install it from https://go.dev/dl/ and run this again." >&2
    exit 1
}

cd "$tools_dir"
go vet ./...
go test ./...

for tool in "${TOOLS[@]}"; do
    pkg="./cmd/$tool"

    if [ "$build_all" = true ]; then
        for target in windows/amd64 linux/amd64 darwin/amd64 darwin/arm64; do
            goos="${target%%/*}"
            goarch="${target##*/}"
            out_dir="$tools_dir/dist/$goos-$goarch"
            mkdir -p "$out_dir"
            bin="$out_dir/$tool"
            [ "$goos" = "windows" ] && bin="$bin.exe"
            echo "Building $tool for $goos/$goarch -> $bin"
            GOOS="$goos" GOARCH="$goarch" go build -o "$bin" "$pkg"
        done
    else
        goos="$(go env GOOS)"
        bin_name="$tool"
        [ "$goos" = "windows" ] && bin_name="$tool.exe"
        while read -r dir; do
            [ -n "$dir" ] || continue
            mkdir -p "$dir"
            echo "Building $tool -> $dir/$bin_name"
            go build -o "$dir/$bin_name" "$pkg"
        done < <(tool_targets "$tool")
    fi
done

echo "Done."

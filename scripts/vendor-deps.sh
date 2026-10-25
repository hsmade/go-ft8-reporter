#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_dir"

go mod vendor

# go mod vendor omits nested directories with no Go package. PocketFFT's
# cgo bridge includes these C sources, so copy them from the pinned module.
module_dir=$(go list -mod=mod -m -f '{{.Dir}}' github.com/ColonelBlimp/go-ft8)
source_dir="$module_dir/internal/pfft/pocketfft"
target_dir=vendor/github.com/ColonelBlimp/go-ft8/internal/pfft/pocketfft
mkdir -p "$target_dir"
for file in pocketfft.c pocketfft.h LICENSE.md README.md VENDORED.md; do
    cp "$source_dir/$file" "$target_dir/$file"
done

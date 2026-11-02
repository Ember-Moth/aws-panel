#!/usr/bin/env sh
set -eu
snapshot_dir=$(mktemp -d)
trap 'rm -rf "$snapshot_dir"' EXIT HUP INT TERM
cp api/generated/swagger.json "$snapshot_dir/swagger.json"
sh scripts/generate-swagger.sh
cmp api/generated/swagger.json "$snapshot_dir/swagger.json"

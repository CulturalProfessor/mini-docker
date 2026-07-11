#!/bin/bash
# build the binary
cd "$(dirname "$0")/.." || exit 1
source scripts/lib.sh

banner "Build"
step "wc -l *.go"
step "go build -o minidoc ."
step "./minidoc"

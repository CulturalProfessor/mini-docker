#!/bin/bash
# runs every script below in order, or just one by name
#   ./scripts/demo.sh              run everything
#   ./scripts/demo.sh network      run only scripts/network.sh
cd "$(dirname "$0")/.." || exit 1

parts=(build pull isolation filesystem limits network cli)

if [ -n "$1" ]; then
    exec "./scripts/$1.sh"
fi

sudo -v
sudo rm -rf containers/* run/* 2>/dev/null
clear

for p in "${parts[@]}"; do
    "./scripts/$p.sh"
done

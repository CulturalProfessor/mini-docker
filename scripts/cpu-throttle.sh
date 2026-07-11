#!/bin/bash
# CPU throttling. Run this in terminal 1, `top` in terminal 2.
cd "$(dirname "$0")/.." || exit 1
source scripts/lib.sh

banner "CPU throttling (open 'top' in a second terminal)"
step "sudo ./minidoc run --cpus 0.5 alpine sh -c 'while true; do :; done'"
# Ctrl-C here to stop the container once you've shown ~50% of a core in top

#!/bin/bash
# filesystem (overlayfs, copy-on-write)
cd "$(dirname "$0")/.." || exit 1
source scripts/lib.sh

banner "Filesystem (overlayfs, copy-on-write)"
step "sudo ./minidoc run alpine sh -c 'cat /etc/os-release; echo; ls /'"
step "sudo ./minidoc run alpine sh -c 'echo hello > /root/note.txt; ls /root'"
step "sudo ./minidoc run alpine ls /root"
step "ls images/alpine/root"

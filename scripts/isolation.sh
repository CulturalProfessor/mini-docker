#!/bin/bash
# isolation (namespaces)
cd "$(dirname "$0")/.." || exit 1
source scripts/lib.sh

banner "Isolation (namespaces)"
step "hostname"
step "sudo ./minidoc run alpine hostname"
step "sudo ./minidoc run alpine ps aux"

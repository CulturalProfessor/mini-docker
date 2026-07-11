#!/bin/bash
# resource limits (cgroups v2)
cd "$(dirname "$0")/.." || exit 1
source scripts/lib.sh

banner "Resource limits (cgroups v2)"
step "sudo ./minidoc run --memory 50m alpine awk 'BEGIN{s=\"\";while(1){s=s sprintf(\"%01000000d\",1)}}'"
step "sudo dmesg | tail -3"
step "sudo ./minidoc run --pids 10 alpine sh -c 'for i in \$(seq 50); do sleep 30 & done; echo done'"

banner "CPU throttling needs two terminals, see scripts/cpu-throttle.sh"

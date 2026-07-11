#!/bin/bash
# networking (veth + bridge + NAT)
cd "$(dirname "$0")/.." || exit 1
source scripts/lib.sh

banner "Networking (veth + bridge + NAT)"
step "sudo ./minidoc run alpine ip addr"
step "sudo ./minidoc run alpine ping -c 3 8.8.8.8"
step "sudo ./minidoc run alpine wget -qO- http://example.com"

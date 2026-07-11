#!/bin/bash
# CLI: live containers + a second image
cd "$(dirname "$0")/.." || exit 1
source scripts/lib.sh

banner "ps lists live containers"
step "sudo ./minidoc run alpine sleep 60 &"
sleep 2
step "./minidoc ps"

banner "A different image, same flow"
step "./minidoc pull busybox"
step "sudo ./minidoc run busybox echo 'second image, straight from a registry'"
step "./minidoc images"

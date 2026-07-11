#!/bin/bash
# pull an image from Docker Hub
cd "$(dirname "$0")/.." || exit 1
source scripts/lib.sh

banner "Pull from Docker Hub"
step "./minidoc pull alpine"
step "./minidoc images"

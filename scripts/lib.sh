#!/bin/bash
# shared helpers for the demo scripts

# print a scene caption
banner() {
    printf '\n\033[1;33m# %s\033[0m\n' "$1"
}

# show a command, wait for Enter, then run it
step() {
    printf '\n\033[1;36m$ %s\033[0m' "$1"
    read -r -s -p '  ' _
    printf '\n'
    eval "$1"
}

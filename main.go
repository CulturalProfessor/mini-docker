package main

import (
	"fmt"
	"os"
)

// minidoc: our small container runtime, "docker run from scratch".
//
// The core trick is that we re-exec ourselves. `run` sets the namespace flags and
// launches /proc/self/exe again in a hidden "child" mode, so the child is born
// inside the new namespaces and we finish setup from there. We have to do it this
// way because Go's runtime is multi-threaded at startup and we can't create some
// namespaces from a multi-threaded process.
func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "run":
		run(os.Args[2:])
	case "pull":
		pull(os.Args[2:])
	case "ps":
		ps(os.Args[2:])
	case "stats":
		stats(os.Args[2:])
	case "images":
		images(os.Args[2:])
	case "child":
		// Internal only: what "run" re-execs into, already inside the new
		// namespaces. We never call this by hand.
		child(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  minidoc pull <image[:tag]>")
	fmt.Fprintln(os.Stderr, "  minidoc run [--memory 100m] [--cpus 0.5] [--pids 64] <image> <command> [args...]")
	fmt.Fprintln(os.Stderr, "  minidoc ps")
	fmt.Fprintln(os.Stderr, "  minidoc stats [id]")
	fmt.Fprintln(os.Stderr, "  minidoc images")
	os.Exit(1)
}

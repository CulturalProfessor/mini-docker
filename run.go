package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// run is the parent side of `minidoc run [flags] <image> <cmd> [args...]`.
// We set up the overlay, cgroup and networking around the child, then let it exec
// the command.
func run(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	memFlag := fs.String("memory", "", "memory limit, e.g. 100m, 512m, 1g (default: unlimited)")
	cpusFlag := fs.Float64("cpus", 0, "CPU cores, e.g. 0.5, 2 (default: unlimited)")
	pidsFlag := fs.Int("pids", 0, "max number of processes (default: unlimited)")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) < 2 {
		usage()
	}

	// rest[0] = image (a rootfs path or a pulled image name), rest[1:] = command.
	// We resolve to an absolute path now because a relative one would mean
	// something else once the child has pivoted.
	image, err := resolveImage(rest[0])
	if err != nil {
		die(err)
	}
	command := rest[1:]

	lim := limits{cpus: *cpusFlag, pids: int64(*pidsFlag)}
	if *memFlag != "" {
		b, err := parseSize(*memFlag)
		if err != nil {
			die(err)
		}
		lim.memoryBytes = b
	}

	id := newID()

	// This container's overlay layers. upper = writes, work = overlay scratch,
	// merged = what the child pivots into.
	containerDir, err := filepath.Abs(filepath.Join("containers", id))
	if err != nil {
		die(err)
	}
	for _, sub := range []string{"upper", "work", "merged"} {
		if err := os.MkdirAll(filepath.Join(containerDir, sub), 0755); err != nil {
			die(err)
		}
	}

	// Create the cgroup and set limits before the process exists.
	leaf, err := cgroupSetup(id, lim)
	if err != nil {
		die(err)
	}

	// Open the cgroup dir so the kernel drops the child straight into it at clone
	// time (CLONE_INTO_CGROUP), which means even its first fork() is limited. We
	// learned the hard way that moving a PID in afterwards is racy and lets
	// already-forked children escape.
	cgFD, err := os.Open(leaf)
	if err != nil {
		cgroupDestroy(leaf)
		die(fmt.Errorf("open cgroup: %w", err))
	}

	// Sync pipe: the child blocks on this until we've built its network, so the
	// command can't run before it has an interface.
	syncR, syncW, err := os.Pipe()
	if err != nil {
		cgFD.Close()
		cgroupDestroy(leaf)
		die(err)
	}

	fmt.Printf("minidoc: starting container %s\n", id)

	childArgs := append([]string{"child", image, containerDir}, command...)
	cmd := exec.Command("/proc/self/exe", childArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{syncR} // becomes fd 3 in the child
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS |
			syscall.CLONE_NEWPID |
			syscall.CLONE_NEWNS |
			syscall.CLONE_NEWIPC |
			syscall.CLONE_NEWNET, // its own network stack
		UseCgroupFD: true,
		CgroupFD:    int(cgFD.Fd()),
	}

	err = cmd.Start()
	cgFD.Close()
	syncR.Close() // the child has its own copy at fd 3
	if err != nil {
		syncW.Close()
		cgroupDestroy(leaf)
		die(err)
	}

	// On Ctrl-C, kill the container. We use SIGKILL because SIGINT/SIGTERM from the
	// host get ignored by a namespaced PID 1 that has no handler for them.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		_ = cmd.Process.Kill()
	}()

	// Build networking now that the child's netns exists (we find it by PID).
	ip, err := setupNetworking(id, cmd.Process.Pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: network: %v\n", err)
		cmd.Process.Kill()
		cmd.Wait()
		syncW.Close()
		cgroupDestroy(leaf)
		os.RemoveAll(containerDir)
		os.Exit(1)
	}

	// Record it so `minidoc ps` can find it.
	if err := writeState(containerState{
		ID:        id,
		PID:       cmd.Process.Pid,
		Image:     image,
		Command:   strings.Join(command, " "),
		IP:        ip,
		StartedAt: time.Now(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "run: warning: %v\n", err)
	}

	// Release the child: closing the write end unblocks its read.
	syncW.Close()

	waitErr := cmd.Wait()

	// Clean up. The netns and veth pair vanish on their own when the container
	// exits, so we just drop the cgroup, state file and overlay layers.
	cgroupDestroy(leaf)
	removeState(id)
	os.RemoveAll(containerDir)

	if waitErr != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", waitErr)
		os.Exit(1)
	}
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "run: %v\n", err)
	os.Exit(1)
}

// resolveImage turns the run argument into an absolute rootfs path. It accepts a
// direct path to a rootfs directory, or the name of an image under images/ (as
// created by `minidoc pull`), with or without a tag.
func resolveImage(arg string) (string, error) {
	if fi, err := os.Stat(arg); err == nil && fi.IsDir() {
		return filepath.Abs(arg)
	}
	// Try images/<name>, and images/<name-without-tag> for e.g. "alpine:3.19".
	names := []string{arg}
	if i := strings.LastIndex(arg, ":"); i > strings.LastIndex(arg, "/") {
		names = append(names, arg[:i])
	}
	for _, n := range names {
		candidate := filepath.Join(imagesDir, n)
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			return filepath.Abs(candidate)
		}
	}
	return "", fmt.Errorf("image %q not found (try: minidoc pull %s)", arg, arg)
}

// newID returns a short random hex id for a container.
func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// parseSize parses sizes like "100m", "512M", "1g", or a raw byte count.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "g"):
		mult, s = 1<<30, strings.TrimSuffix(s, "g")
	case strings.HasSuffix(s, "m"):
		mult, s = 1<<20, strings.TrimSuffix(s, "m")
	case strings.HasSuffix(s, "k"):
		mult, s = 1<<10, strings.TrimSuffix(s, "k")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * mult, nil
}

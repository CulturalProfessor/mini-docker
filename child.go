package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// child runs inside the new namespaces.
// args are [<image>, <containerDir>, <cmd>, <cmd-args>...].
func child(args []string) {
	image := args[0]
	containerDir := args[1]
	command := args[2:]

	// Our own hostname; the host's is untouched (UTS namespace).
	must(syscall.Sethostname([]byte("container")))

	// Build the copy-on-write root and pivot into it.
	must(setupRootfs(image, containerDir))

	// Mount the virtual filesystems now that we're in the new root.
	must(mountProc())
	must(mountDev())

	// Block until the parent has wired up our network. Until then the netns only
	// has a down loopback.
	waitForNetwork()

	// Set DNS so name lookups work.
	must(writeResolvConf())

	// Become the real command. LookPath because execve won't search $PATH. This
	// process is PID 1 inside the container.
	path, err := exec.LookPath(command[0])
	must(err)
	must(syscall.Exec(path, command, os.Environ()))
}

// setupRootfs stacks an overlay and pivots into it. We keep the image as a shared
// read-only lower layer and send each container's writes to its own upper, so the
// image never changes and containers can't see each other's writes.
func setupRootfs(image, containerDir string) error {
	upper := filepath.Join(containerDir, "upper")
	work := filepath.Join(containerDir, "work")
	merged := filepath.Join(containerDir, "merged")

	// Make mounts private so nothing we do leaks to the host. Also required, or
	// pivot_root fails with EINVAL on a shared /.
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make / private: %w", err)
	}

	// lower = image (read-only), upper = our writes, work = overlay scratch (same
	// fs as upper). merged is the combined view.
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", image, upper, work)
	if err := syscall.Mount("overlay", merged, "overlay", 0, opts); err != nil {
		return fmt.Errorf("mount overlay: %w", err)
	}

	return pivotRoot(merged)
}

// pivotRoot makes newroot our / and detaches the old root.
//
// We use pivot_root over chroot because chroot only changes where lookups start,
// and CAP_SYS_ADMIN can escape it. pivot_root swaps the real root mount, and once
// we unmount the old one there's nothing left to escape to. newroot has to be a
// mount point (the overlay mount is) and / has to be private (done above).
func pivotRoot(newroot string) error {
	oldroot := filepath.Join(newroot, ".old_root")
	if err := os.MkdirAll(oldroot, 0700); err != nil {
		return fmt.Errorf("mkdir .old_root: %w", err)
	}

	// newroot becomes /, the old root moves to /.old_root.
	if err := syscall.PivotRoot(newroot, oldroot); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := syscall.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}

	// Lazy-unmount the old root and drop the stub so we can't reach the host
	// filesystem from inside.
	if err := syscall.Unmount("/.old_root", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("unmount old root: %w", err)
	}
	if err := os.Remove("/.old_root"); err != nil {
		return fmt.Errorf("remove .old_root: %w", err)
	}
	return nil
}

// mountProc mounts a fresh proc so /proc reflects our PID namespace, not the
// host's. Without it, `ps` still lists host processes. Some images (busybox)
// don't ship a /proc dir, so we create the mountpoint first.
func mountProc() error {
	if err := os.MkdirAll("/proc", 0555); err != nil {
		return fmt.Errorf("mkdir /proc: %w", err)
	}
	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		return fmt.Errorf("mount /proc: %w", err)
	}
	return nil
}

// mountDev builds a minimal /dev. Alpine's minirootfs ships /dev empty, so
// without this even `> /dev/null` fails. A device node is just a file tagged with
// a (major, minor) pair that the kernel maps to a driver. We create the mountpoint
// first since not every image includes /dev.
func mountDev() error {
	if err := os.MkdirAll("/dev", 0755); err != nil {
		return fmt.Errorf("mkdir /dev: %w", err)
	}
	if err := syscall.Mount("tmpfs", "/dev", "tmpfs", syscall.MS_NOSUID, "mode=0755"); err != nil {
		return fmt.Errorf("mount /dev: %w", err)
	}

	nodes := []struct {
		path         string
		major, minor int
	}{
		{"/dev/null", 1, 3},
		{"/dev/zero", 1, 5},
		{"/dev/full", 1, 7},
		{"/dev/random", 1, 8},
		{"/dev/urandom", 1, 9},
		{"/dev/tty", 5, 0},
	}
	for _, n := range nodes {
		// S_IFCHR = character device; the device number packs (major<<8 | minor).
		dev := n.major<<8 | n.minor
		if err := syscall.Mknod(n.path, syscall.S_IFCHR|0666, dev); err != nil {
			return fmt.Errorf("mknod %s: %w", n.path, err)
		}
	}
	return nil
}

// waitForNetwork blocks until the parent closes the sync pipe (eth0 is ready).
// The parent handed us the read end as our first extra fd (fd 3).
func waitForNetwork() {
	f := os.NewFile(3, "network-sync")
	if f == nil {
		return
	}
	defer f.Close()
	var buf [1]byte
	_, _ = f.Read(buf[:]) // returns when the parent closes the pipe (EOF)
}

// writeResolvConf sets our DNS resolvers in the container's own /etc/resolv.conf.
func writeResolvConf() error {
	const conf = "nameserver 8.8.8.8\nnameserver 1.1.1.1\n"
	if err := os.WriteFile("/etc/resolv.conf", []byte(conf), 0644); err != nil {
		return fmt.Errorf("write /etc/resolv.conf: %w", err)
	}
	return nil
}

// must bails out of the child with a clear message if a setup step fails.
func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "child: %v\n", err)
		os.Exit(1)
	}
}

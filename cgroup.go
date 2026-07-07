package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cgroupBase is the mount point of the cgroup v2 unified hierarchy.
const cgroupBase = "/sys/fs/cgroup"

// limits are the resource caps for a container. Zero means "no limit".
type limits struct {
	memoryBytes int64   // memory.max
	cpus        float64 // cpu.max, in fractions of a core (0.5 = half a core)
	pids        int64   // pids.max
}

// cgroupSetup makes the container's cgroup at /sys/fs/cgroup/minidoc/<id>, turns
// on the controllers we need, applies the limits, and returns the path.
//
// In cgroup v2 a controller only works in a child if the parent lists it in
// cgroup.subtree_control. The root already delegates cpu/memory/pids (systemd did
// that), so we only delegate one more level, from our "minidoc" cgroup down to each
// container's leaf.
func cgroupSetup(id string, lim limits) (string, error) {
	parent := filepath.Join(cgroupBase, "minidoc")
	leaf := filepath.Join(parent, id)

	if err := os.MkdirAll(parent, 0755); err != nil {
		return "", fmt.Errorf("mkdir cgroup parent: %w", err)
	}
	if err := enableControllers(parent, []string{"cpu", "memory", "pids"}); err != nil {
		return "", err
	}
	if err := os.MkdirAll(leaf, 0755); err != nil {
		return "", fmt.Errorf("mkdir cgroup leaf: %w", err)
	}

	if lim.memoryBytes > 0 {
		if err := writeCgroup(leaf, "memory.max", strconv.FormatInt(lim.memoryBytes, 10)); err != nil {
			return "", err
		}
		// Forbid swap so memory.max is a hard wall. Otherwise, with swap on, the
		// container spills over instead of getting OOM-killed.
		_ = writeCgroup(leaf, "memory.swap.max", "0")
	}
	if lim.cpus > 0 {
		// cpu.max is "<quota> <period>" microseconds: run for quota out of every
		// period. 50000/100000 = half a core. Going over throttles, doesn't kill.
		const period = 100000
		quota := int64(lim.cpus * period)
		if err := writeCgroup(leaf, "cpu.max", fmt.Sprintf("%d %d", quota, period)); err != nil {
			return "", err
		}
	}
	if lim.pids > 0 {
		if err := writeCgroup(leaf, "pids.max", strconv.FormatInt(lim.pids, 10)); err != nil {
			return "", err
		}
	}
	return leaf, nil
}

// cgroupDestroy rmdir's the leaf. It has to be empty of processes, which it is
// once the container has exited.
func cgroupDestroy(leaf string) error {
	return os.Remove(leaf)
}

// enableControllers turns on any missing controllers for a cgroup's children by
// writing "+cpu +memory ..." to its cgroup.subtree_control.
func enableControllers(dir string, want []string) error {
	current, err := os.ReadFile(filepath.Join(dir, "cgroup.subtree_control"))
	if err != nil {
		return fmt.Errorf("read subtree_control: %w", err)
	}
	have := map[string]bool{}
	for _, c := range strings.Fields(string(current)) {
		have[c] = true
	}
	var add []string
	for _, c := range want {
		if !have[c] {
			add = append(add, "+"+c)
		}
	}
	if len(add) == 0 {
		return nil
	}
	if err := writeCgroup(dir, "cgroup.subtree_control", strings.Join(add, " ")); err != nil {
		return fmt.Errorf("enable controllers %v: %w", add, err)
	}
	return nil
}

// writeCgroup writes a value to a cgroup control file.
func writeCgroup(dir, file, value string) error {
	if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0644); err != nil {
		return fmt.Errorf("write %s=%q: %w", file, value, err)
	}
	return nil
}

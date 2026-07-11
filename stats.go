package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// cgroupSample is one snapshot of a container's cgroup counters.
type cgroupSample struct {
	memCurrent  int64  // bytes
	memMax      int64  // bytes, -1 if unlimited
	pidsCurrent int64
	pidsMax     int64  // -1 if unlimited
	cpuUsec     uint64 // cumulative CPU time, from cpu.stat's usage_usec
}

// stats prints a live resource snapshot for running containers, in the spirit of
// `docker stats`. cgroups only expose cumulative CPU time, not a rate, so we read
// cpu.stat, wait a beat, read it again, and divide the delta by wall time. CPU% is
// out of one core (a container with --cpus 2 maxing out both shows 200%).
func stats(args []string) {
	states, err := listStates()
	if err != nil {
		die(err)
	}
	if len(args) > 0 {
		id := args[0]
		filtered := states[:0]
		for _, s := range states {
			if s.ID == id {
				filtered = append(filtered, s)
			}
		}
		states = filtered
		if len(states) == 0 {
			die(fmt.Errorf("no running container %q", id))
		}
	}
	if len(states) == 0 {
		fmt.Println("no running containers")
		return
	}

	before := map[string]cgroupSample{}
	for _, s := range states {
		before[s.ID], _ = readCgroupSample(s.ID)
	}
	const interval = 200 * time.Millisecond
	time.Sleep(interval)

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 3, ' ', 0)
	fmt.Fprintln(w, "CONTAINER ID\tCPU %\tMEM USAGE / LIMIT\tMEM %\tPIDS")
	for _, s := range states {
		after, err := readCgroupSample(s.ID)
		if err != nil {
			fmt.Fprintf(w, "%s\terror: %v\n", s.ID, err)
			continue
		}
		prev := before[s.ID]
		var cpuPct float64
		if after.cpuUsec >= prev.cpuUsec {
			cpuPct = float64(after.cpuUsec-prev.cpuUsec) / float64(interval.Microseconds()) * 100
		}

		memLimit, memPct := "unlimited", "-"
		if after.memMax >= 0 {
			memLimit = humanSize(after.memMax)
			memPct = fmt.Sprintf("%.1f%%", float64(after.memCurrent)/float64(after.memMax)*100)
		}
		pidsLimit := "unlimited"
		if after.pidsMax >= 0 {
			pidsLimit = strconv.FormatInt(after.pidsMax, 10)
		}

		fmt.Fprintf(w, "%s\t%.1f%%\t%s / %s\t%s\t%d / %s\n",
			s.ID, cpuPct, humanSize(after.memCurrent), memLimit, memPct, after.pidsCurrent, pidsLimit)
	}
	w.Flush()
}

// readCgroupSample reads the current counters for a container's cgroup leaf.
func readCgroupSample(id string) (cgroupSample, error) {
	leaf := filepath.Join(cgroupBase, "minidoc", id)
	sample := cgroupSample{memMax: -1, pidsMax: -1}

	sample.memCurrent, _ = readCgroupInt(leaf, "memory.current")
	if v, ok := readCgroupMaybeMax(leaf, "memory.max"); ok {
		sample.memMax = v
	}
	sample.pidsCurrent, _ = readCgroupInt(leaf, "pids.current")
	if v, ok := readCgroupMaybeMax(leaf, "pids.max"); ok {
		sample.pidsMax = v
	}
	usage, err := readCPUUsageUsec(leaf)
	if err != nil {
		return sample, err
	}
	sample.cpuUsec = usage
	return sample, nil
}

// readCgroupInt reads a cgroup file holding a single integer.
func readCgroupInt(dir, file string) (int64, error) {
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
}

// readCgroupMaybeMax reads a cgroup file that's either an integer or the literal
// "max" (cgroup v2's spelling of "no limit").
func readCgroupMaybeMax(dir, file string) (int64, bool) {
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(data))
	if s == "max" {
		return -1, true
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readCPUUsageUsec reads usage_usec out of cpu.stat, which looks like:
//
//	usage_usec 123456
//	user_usec 100000
//	system_usec 23456
func readCPUUsageUsec(dir string) (uint64, error) {
	f, err := os.Open(filepath.Join(dir, "cpu.stat"))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[0] == "usage_usec" {
			return strconv.ParseUint(fields[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("usage_usec not found in cpu.stat")
}

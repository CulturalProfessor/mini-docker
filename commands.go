package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"
)

// imagesDir is where extracted root filesystems live, one directory per image.
const imagesDir = "images"

// ps prints the containers that are currently running.
func ps(_ []string) {
	states, err := listStates()
	if err != nil {
		die(err)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 3, ' ', 0)
	fmt.Fprintln(w, "CONTAINER ID\tIP\tUPTIME\tCOMMAND")
	for _, s := range states {
		uptime := time.Since(s.StartedAt).Round(time.Second)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.ID, s.IP, uptime, s.Command)
	}
	w.Flush()
}

// images lists the root filesystems available under the images directory.
func images(_ []string) {
	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("no images (create one under %s/)\n", imagesDir)
			return
		}
		die(err)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 3, ' ', 0)
	fmt.Fprintln(w, "IMAGE\tSIZE")
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\n", e.Name(), humanSize(dirSize(filepath.Join(imagesDir, e.Name()))))
	}
	w.Flush()
}

// dirSize sums the sizes of all regular files under root (best-effort).
func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries rather than failing
		}
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// humanSize formats a byte count as a short human-readable string.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGT"[exp])
}

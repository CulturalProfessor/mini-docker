package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// stateDir holds one JSON file per running container so `minidoc ps` (a separate
// process) can see what's running. I write a file when a container starts and
// remove it when it exits.
const stateDir = "run"

// containerState is the metadata recorded for a running container.
type containerState struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"` // host-side PID (used to check liveness)
	Image     string    `json:"image"`
	Command   string    `json:"command"`
	IP        string    `json:"ip"`
	StartedAt time.Time `json:"started_at"`
}

// writeState records a running container to the state directory.
func writeState(s containerState) error {
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir, s.ID+".json"), data, 0644)
}

// removeState deletes a container's state file (best-effort).
func removeState(id string) {
	_ = os.Remove(filepath.Join(stateDir, id+".json"))
}

// listStates returns every recorded container, pruning any whose process is gone
// (e.g. minidoc got killed before it could clean up).
func listStates() ([]containerState, error) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // nothing has run yet
		}
		return nil, err
	}
	var states []containerState
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(stateDir, e.Name()))
		if err != nil {
			continue
		}
		var s containerState
		if json.Unmarshal(data, &s) != nil {
			continue
		}
		if !pidAlive(s.PID) {
			removeState(s.ID) // stale entry from a crash
			continue
		}
		states = append(states, s)
	}
	return states, nil
}

// pidAlive reports whether a PID still exists. Signal 0 does the kernel's
// existence/permission check without sending anything: nil means it exists and I
// can signal it; EPERM means it exists but I can't (e.g. `ps` as non-root against
// a root container); only ESRCH means it's really gone.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

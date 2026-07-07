package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Networking: one host bridge acts as a switch and gateway for every container on
// a private /24. I connect each container with a veth pair (a virtual cable): one
// end on the bridge, the other inside the container as eth0. Outbound traffic is
// NAT'd (MASQUERADE) so a private IP can reach the internet behind the host.
const (
	bridgeName = "minidoc0"
	bridgeAddr = "10.10.0.1/24"
	subnet     = "10.10.0.0/24"
	gatewayIP  = "10.10.0.1"
)

// setupNetworking builds the bridge (once) and connects this container, returning
// its IP. I use pid to find the container's network namespace.
func setupNetworking(id string, pid int) (string, error) {
	if err := ensureBridge(); err != nil {
		return "", err
	}
	ip, err := connectContainer(id, pid)
	if err != nil {
		return "", err
	}
	fmt.Printf("minidoc: container %s network up: %s (gw %s)\n", id, ip, gatewayIP)
	return ip, nil
}

// ensureBridge creates the bridge if it's missing, turns on IP forwarding, and
// installs the NAT and forwarding rules. Everything here is idempotent.
func ensureBridge() error {
	if !linkExists(bridgeName) {
		// A bridge is a virtual L2 switch in the kernel.
		if err := sh("ip", "link", "add", bridgeName, "type", "bridge"); err != nil {
			return err
		}
		// The gateway address containers route through.
		if err := sh("ip", "addr", "add", bridgeAddr, "dev", bridgeName); err != nil {
			return err
		}
		if err := sh("ip", "link", "set", bridgeName, "up"); err != nil {
			return err
		}
	}

	// The host has to route between interfaces, or forwarded packets get dropped.
	if err := sh("sysctl", "-q", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return err
	}

	// NAT: rewrite the source of container traffic leaving a non-bridge interface
	// to the host IP, so replies can get back. This is what lets a private IP
	// reach the internet.
	if err := ensureRule(false, "nat", "POSTROUTING",
		"-s", subnet, "!", "-o", bridgeName, "-j", "MASQUERADE"); err != nil {
		return err
	}

	// Allow forwarding to/from the bridge. I insert at the top of FORWARD in case
	// the default policy is DROP (my host has Docker, so it is).
	if err := ensureRule(true, "filter", "FORWARD", "-i", bridgeName, "-j", "ACCEPT"); err != nil {
		return err
	}
	if err := ensureRule(true, "filter", "FORWARD", "-o", bridgeName, "-j", "ACCEPT"); err != nil {
		return err
	}
	return nil
}

// connectContainer makes a veth pair, plugs one end into the bridge, moves the
// other into the container's netns, and sets it up as eth0. Returns the IP.
func connectContainer(id string, pid int) (string, error) {
	host := "veth" + id  // host end (must be <= 15 chars, IFNAMSIZ)
	peer := "vpeer" + id // container end
	ip := containerIP(id)

	// The virtual cable: two interfaces wired to each other.
	if err := sh("ip", "link", "add", host, "type", "veth", "peer", "name", peer); err != nil {
		return "", err
	}
	// Plug the host end into the bridge and bring it up.
	if err := sh("ip", "link", "set", host, "master", bridgeName); err != nil {
		return "", err
	}
	if err := sh("ip", "link", "set", host, "up"); err != nil {
		return "", err
	}
	// Move the other end into the container's netns, found via its PID.
	if err := sh("ip", "link", "set", peer, "netns", strconv.Itoa(pid)); err != nil {
		return "", err
	}

	// Configure it from inside the netns. nsenter -n enters just the net ns and
	// runs the host's ip in there.
	nsSteps := [][]string{
		{"ip", "link", "set", peer, "name", "eth0"},
		{"ip", "addr", "add", ip + "/24", "dev", "eth0"},
		{"ip", "link", "set", "eth0", "up"},
		{"ip", "link", "set", "lo", "up"},
		{"ip", "route", "add", "default", "via", gatewayIP},
	}
	for _, step := range nsSteps {
		args := append([]string{"-t", strconv.Itoa(pid), "-n"}, step...)
		if err := sh("nsenter", args...); err != nil {
			return "", err
		}
	}
	return ip, nil
}

// containerIP picks a last octet in [2,254] from the id. It's a toy allocator:
// fine for a few containers, but two ids could collide. Good enough for now.
func containerIP(id string) string {
	n := 2
	if v, err := strconv.ParseInt(id[:2], 16, 0); err == nil {
		n = 2 + int(v)%253
	}
	return fmt.Sprintf("10.10.0.%d", n)
}

// linkExists reports whether a network interface with this name exists.
func linkExists(name string) bool {
	return exec.Command("ip", "link", "show", name).Run() == nil
}

// ensureRule adds an iptables rule if it isn't already there (checked with -C, so
// repeat runs don't stack duplicates). insert=true prepends (-I) instead of
// appending (-A).
func ensureRule(insert bool, table, chain string, rule ...string) error {
	check := append([]string{"-t", table, "-C", chain}, rule...)
	if exec.Command("iptables", check...).Run() == nil {
		return nil
	}
	op := "-A"
	if insert {
		op = "-I"
	}
	add := append([]string{"-t", table, op, chain}, rule...)
	return sh("iptables", add...)
}

// sh runs a command and returns its output on failure. I shell out to
// ip/iptables/nsenter for networking so every step is a command I could run by
// hand and inspect.
func sh(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

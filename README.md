# minidoc

A small container runtime in Go: *"docker run from scratch"*. It builds the Linux
primitives that Docker and containerd sit on top of, straight from the syscall
level: namespaces, `pivot_root`, overlayfs, cgroups v2, and veth/bridge/NAT
networking.

```
sudo ./minidoc run <image> <command>
```

> Learning project. Linux only (built and tested on kernel 6.8). Needs root.

## What it does

Runs a process in a real container with:

- **Isolation** via namespaces: PID, mount, UTS (hostname), IPC, and network.
- **Its own root filesystem** via `pivot_root` into an Alpine rootfs.
- **Copy-on-write layers** via overlayfs (shared read-only image + per-container
  writable layer).
- **Resource limits** via cgroups v2: memory, CPU, and process count.
- **Networking**: its own IP on a host bridge, with NAT out to the internet.
- **A small CLI**: `run`, `ps`, `images`.

Everything above the shell-outs to `ip`/`iptables`/`nsenter` is built directly on
the standard library and raw syscalls, no third-party dependencies.

## Quick start

```sh
go build -o minidoc .

# One-time: fetch an Alpine rootfs (~3 MB) into images/alpine
mkdir -p images/alpine
curl -sL https://dl-cdn.alpinelinux.org/alpine/latest-stable/releases/x86_64/ \
  | grep -o 'alpine-minirootfs-[0-9.]*-x86_64.tar.gz' | sort -uV | tail -1 \
  | xargs -I{} curl -sL "https://dl-cdn.alpinelinux.org/alpine/latest-stable/releases/x86_64/{}" \
  | tar -xz -C images/alpine

# A shell inside the container
sudo ./minidoc run ./images/alpine /bin/sh

# With resource limits
sudo ./minidoc run --memory 100m --cpus 0.5 --pids 64 ./images/alpine /bin/sh

# Networking is automatic: own IP, reaches the internet
sudo ./minidoc run ./images/alpine ping -c 3 8.8.8.8
sudo ./minidoc run ./images/alpine wget -qO- http://example.com
```

## Architecture

```
  sudo ./minidoc run [--memory/--cpus/--pids] <image> <command>
        |
   parent: make overlay dirs + a cgroup, then re-exec itself:
        |   clone3( CLONE_NEWUTS|NEWPID|NEWNS|NEWIPC|NEWNET , INTO_CGROUP )
        v
  +------------------------------------------------------------+
  | child  (PID 1 inside)     ns: UTS . PID . MOUNT . IPC . NET |
  |                                                            |
  |   sethostname -> overlay(lower=image, upper, work)         |
  |   -> pivot_root -> mount /proc, /dev                       |
  |   -> wait for network -> write /etc/resolv.conf -> exec    |
  +------------------------------------------------------------+
        |                                    ^
        | held by cgroup:                    | parent wires up veth + IP,
        | memory.max . cpu.max . pids.max    | then releases child (sync pipe)
        v                                    |
   eth0 (10.10.0.x) -- veth --> bridge minidoc0 (10.10.0.1)
                                    |
                               MASQUERADE + ip_forward=1
                                    |
                                    v
                               host NIC --> internet
```

One concern per file:

| File | Responsibility |
|------|----------------|
| `main.go` | Command dispatch (`run` / `ps` / `images` / internal `child`). |
| `run.go` | Parent: flags, overlay dirs, cgroup, launch, networking, cleanup. |
| `child.go` | In-namespace setup: hostname, rootfs, `/proc`, `/dev`, exec. |
| `cgroup.go` | cgroup v2 creation and limits. |
| `network.go` | Bridge, veth pair, NAT. |
| `state.go`, `commands.go` | Container state and the `ps` / `images` commands. |

## Commands

| Command | What it does |
|---------|--------------|
| `run [--memory 100m] [--cpus 0.5] [--pids 64] <image> <cmd>` | Run `<cmd>` in a fresh container. |
| `ps` | List running containers (id, IP, uptime, command). |
| `images` | List root filesystems under `images/`. |

Run `ps` from a second terminal while a container is up:

```sh
$ ./minidoc ps
CONTAINER ID   IP           UPTIME   COMMAND
04a9823d       10.10.0.6    12s      sleep 60
```

## How it works

Each piece explains a mechanism and why it's there.

### The re-exec pattern

`minidoc run` doesn't run your command directly. It re-execs its own binary
(`/proc/self/exe`) in a hidden `child` mode, passing namespace flags to `clone`
so the child is born inside fresh namespaces. The child finishes setup (hostname,
mounts, root pivot) and then `execve`s your command. This is needed because Go is
multi-threaded at startup and can't create some namespaces in-process.

### PID, mount and IPC namespaces

`CLONE_NEWPID` gives the container its own PID space, so the command runs as
PID 1. `CLONE_NEWNS` gives it a private mount table: we remount `/` as
`MS_PRIVATE` so nothing leaks to the host, then mount a fresh `proc` over
`/proc`. That remount is what makes `ps` show only container processes, because
`/proc` renders PIDs from the caller's PID namespace. `CLONE_NEWIPC` isolates
System V IPC.

### pivot_root into a rootfs

`chroot` only changes where path lookups start, and a process with
`CAP_SYS_ADMIN` can escape it. `pivot_root` swaps the actual root mount: the
container's rootfs becomes `/`, the old root moves to `/.old_root`, and we then
lazily unmount and delete it so there's nothing left to escape to. The new root
must be a mount point and `/` must be private, or `pivot_root` returns `EINVAL`.
Afterwards we mount a fresh `/proc` and build a minimal `/dev`, since Alpine's
minirootfs ships `/dev` empty.

### overlayfs (copy-on-write root)

Each container gets an overlay mount from three directories: the image as a
read-only `lowerdir`, a per-container `upperdir` for writes, and a `workdir` for
overlay's scratch. We pivot into the `merged` view. Reads fall through to lower;
the first write to a file triggers copy-up (copied to upper, then modified);
deleting a lower file records a whiteout in upper. The image is never changed and
each container's writes stay in its own `containers/<id>/upper`. This is how
Docker's image layers and per-container writable layer work.

### cgroups v2 (resource limits)

Namespaces isolate what a process can see; cgroups limit what it can use. minidoc
creates a cgroup per container at `/sys/fs/cgroup/minidoc/<id>`, delegates the
`cpu`/`memory`/`pids` controllers to it via `cgroup.subtree_control`, and applies:

- `memory.max`: hard ceiling; exceeding it triggers an OOM kill. We also set
  `memory.swap.max=0` so the cap can't be dodged via swap.
- `cpu.max`: `"quota period"` microseconds (`50000 100000` = half a core).
  Over-users are throttled, not killed.
- `pids.max`: process cap; `fork()` returns `EAGAIN` past it.

The container is spawned straight into the cgroup with
`clone3(CLONE_INTO_CGROUP)` (Go's `SysProcAttr.UseCgroupFD`), so even its first
`fork()` is limited. Moving a PID into `cgroup.procs` after the fact is racy and
lets already-forked children escape.

### Networking (veth + bridge + NAT)

`CLONE_NEWNET` gives the container an empty network stack, so the host builds its
connectivity from the outside:

1. **Bridge**: a virtual L2 switch, `minidoc0` at `10.10.0.1/24`, created once
   and shared as the gateway.
2. **veth pair**: a virtual cable. One end plugs into the bridge; the other moves
   into the container's netns (found by PID) and becomes `eth0` with an IP and a
   default route.
3. **NAT**: an `iptables` MASQUERADE rule rewrites the source of outbound
   container traffic to the host IP, and `ip_forward=1` lets the host route it.
4. **DNS**: the container gets its own `/etc/resolv.conf`.

A sync pipe makes the child wait until its interface is ready before it execs, so
the command never runs without a network. When the container exits, its netns is
destroyed and the veth pair goes with it.

### CLI and lifecycle

`run` is foreground: it blocks until the container exits, then removes the cgroup,
the state file, and the overlay layers. A JSON file per container under `run/`
lets `ps` (a separate process) list what's live. Ctrl-C is turned into a
`SIGKILL` for the container, because a `SIGINT` from the host is ignored by a
namespaced PID 1 with no handler.

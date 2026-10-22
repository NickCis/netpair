# netpair

Run process pairs in isolated network namespaces with Internet access, local port relay, and native SSH port forwarding.

Each **pair** gets its own network stack via [pasta](https://passt.top/) (from the `passt` package). A service and any other process (browser, shell command, etc.) inside the same pair share that namespace, so both can use `127.0.0.1:8080` independently of other pairs.

## Requirements

- Linux with unprivileged user namespaces enabled (see
  [Ubuntu 24.04 note](#ubuntu-2404-unprivileged-user-namespaces--apparmor))
- [passt](https://passt.top/) — provides `pasta` (`apt install passt`)
- [iproute2](https://wiki.linuxfoundation.org/networking/iproute2) — provides `ss` for automatic port discovery in `connect`
- Go 1.26+ (to build from source)

If something does not start as expected, run `netpair doctor` first.

## Build from source

```bash
make build
# produces ./netpair and ./netpair-manager in the project root
```

Both binaries must be on `PATH` together, or `netpair-manager` must sit next to `netpair`.

## Packaging

Packaging files live under [`package/`](package/).

### Ubuntu / Debian

#### Runtime dependencies

To run netpair after installing the `.deb`:

```bash
sudo apt install passt iproute2
```

| Package | Purpose |
|---------|---------|
| `passt` | Provides `pasta` for isolated network namespaces |
| `iproute2` | Provides `ss` for `netpair connect` auto-discovery |

#### Build dependencies (Ubuntu 24.04 Noble)

On **24.04**, all packages are in the default repos (main + universe). No extra PPAs are required.

```bash
sudo apt update
sudo apt install \
  debhelper \
  devscripts \
  golang-1.26-go \
  passt \
  iproute2
```

| Package | Repo | Notes |
|---------|------|-------|
| `debhelper` | main | Required by `debian/rules` |
| `devscripts` | main | Provides `dpkg-buildpackage` |
| `golang-1.26-go` | universe | **Use this, not `golang-go`** |
| `passt` | universe | Runtime dep; useful for testing |
| `iproute2` | main | Runtime dep |

If `passt` is not found, enable universe:

```bash
sudo add-apt-repository universe
sudo apt update
```

**Go version:** `go.mod` requires Go 1.26. Do not install `golang-go` alone — it points to Go 1.22 on Noble. Install `golang-1.26-go`; `debian/rules` adds `/usr/lib/go-1.26/bin` to `PATH` automatically.

```bash
./package/ubuntu/build-deb.sh
```

Install the resulting package from the parent directory:

```bash
sudo apt install ../netpair_0.1.6-1_*.deb
```

See also [`package/ubuntu/README.md`](package/ubuntu/README.md).

#### Ubuntu 22.04 (Jammy) and older

| Package | Jammy 22.04 |
|---------|-------------|
| `debhelper`, `golang-go`, `iproute2` | Available |
| **`passt`** | **Not in Ubuntu repos** |

Workarounds for missing `passt` on Jammy:

1. Upgrade to 24.04 (simplest).
2. Build [passt from source](https://passt.top/).
3. Try the Noble `.deb` (libc-dependent):
   ```bash
   wget http://archive.ubuntu.com/ubuntu/pool/universe/p/passt/passt_0.0~git20240220.1e6f92b-1_amd64.deb
   sudo dpkg -i passt_*.deb
   ```

There is no official PPA for `passt` on Jammy.

### Arch Linux (AUR)

Runtime: `passt`. Build: `go`. Optional: `iproute2` for `connect` auto-discovery.

```bash
cd package/aur
makepkg -si
```

The PKGBUILD clones tag `v0.1.6` into `src/netpair`; the tag must exist on the remote. See [`package/aur/README.md`](package/aur/README.md).

## Quick start

```bash
# Create an isolated pair
netpair create alpha

# Run a process inside the namespace (blocking, stdio attached) or in the
# background (--daemon, output to the pair log)
netpair run alpha --daemon -- python3 -m http.server 8080

# Expose namespace ports on the host (blocking)
netpair connect alpha --port 8080

# In another terminal on the host
curl http://127.0.0.1:8080

# List / destroy
netpair ps alpha
netpair destroy alpha
```

Everything after `--` is passed directly to `exec` — no shell, no special-casing.

### Running processes

By default `netpair run` is blocking and attached: stdin is forwarded to the process, its stdout and stderr are merged (in order) onto the caller's output, and the command exits with the process's exit code. Ctrl-C terminates the process (a second Ctrl-C exits immediately).

There is no TTY inside the pair, so full-screen interactive programs are not supported.

With `--daemon` the process is started in the background instead: `netpair run` prints its id/pid, its output goes to the pair log, and it can be managed with `netpair ps` / `netpair kill`.

```bash
netpair run alpha -- watch -n1 date        # blocks, Ctrl-C to stop
netpair run alpha --daemon -- python3 -m http.server 8080
tail -f "$XDG_RUNTIME_DIR/netpair/alpha.log"
```

`netpair ps` lists managed processes with a header row:

```
ID      NS-PID  HOST-PID  STATUS      COMMAND
proc-1  9       162870    running     python3 -m http.server 8080
proc-2  10      -         exited(42)  /bin/sh -c ...
```

A pair has its own PID namespace, so `NS-PID` only exists inside the pair; `HOST-PID` is the real process on the host (use it with regular host tools such as `kill`, `strace` or `cat /proc/<pid>/...`). It shows `-` for processes that already exited or that cannot be resolved.

`netpair kill <name> --id proc-N` signals a managed process through the manager (default `SIGKILL`); add `--signal TERM` (a name or a number) for a graceful stop. Because `netpair ps` reports host PIDs, you can also signal a running process directly from the host with `kill <HOST-PID>`.

### User and group IDs

pasta creates the pair's user namespace with the invoking user mapped to UID 0, so a process started directly inside it would see itself as root. netpair-manager starts every managed process in a **nested user namespace mapped to your real UID/GID** instead. Inside a pair:

- `id` reports your normal user and group IDs;
- `getpwuid($(id -u))` resolves to your account, so `~` is your home directory;
- files you own on the host are owned by you, and applications that refuse to run as root (Chromium, for example) work.

The manager itself remains root inside the pair's user namespace, because it creates the socket, bind-mounts the corrected resolv.conf and relays connections; only managed processes take your identity.

Trade-offs: managed processes run without capabilities over the pair's namespaces. Configuring interfaces, mounting filesystems and raw sockets are unavailable (`ping` may fail depending on the host's `net.ipv4.ping_group_range`); they can still create their own nested namespaces, which sandboxed applications use. Binding low ports still works: the manager raises `net.ipv4.ip_unprivileged_port_start` to 0 in the pair's own network namespace.

Host-root-owned files still appear as `nobody` inside the pair, since only your UID is mapped. Programs that enforce configuration ownership can trip over this: OpenSSH rejects included system config files with `Bad owner or permissions on /etc/ssh/ssh_config.d/...`. Use `ssh -F ~/.ssh/config` (or `-F /dev/null`) inside pairs to skip the system config.

## Commands

| Command | Description |
|---------|-------------|
| `create <name>` | Start pasta + manager for a new pair |
| `destroy <name>` | Tear down a pair |
| `list` | List pairs |
| `status <name>` | Show pair state (JSON) |
| `doctor` | Check the environment (pasta, user namespaces, DNS, ...) |
| `run <name> -- <argv...>` | Run a process with stdio attached (blocking, exits with its code) |
| `run <name> --daemon -- <argv...>` | Start a detached process (output goes to the pair log) |
| `kill <name> --id <id> [--signal SIG]` | Signal a managed process (default SIGKILL) |
| `ps <name>` | List managed processes (in-pair and host PIDs) |
| `connect <name>` | Auto-discover listening ports and forward to host |
| `connect <name> --port 8080` | Forward ns:8080 → host:8080 |
| `connect <name> --port 8080:19080` | Forward ns:8080 → host:19080 |
| `ssh-tunnel <name> --remote user@host ...` | SSH `-R` (reverse forward) |
| `ssh-remote --remote user@host ...` | SSH `-L` (local forward), optional `-- <argv...>` |

Options:

- `--verbose` / `-v` (or `NETPAIR_VERBOSE=1`): stream the pair's log file to stderr while `create` waits for pasta and the manager to come up, so startup errors and warnings are visible in the terminal instead of only in `<name>.log`. Only accepted before a `--` separator.
- `--daemon` (`run` only): detach instead of running attached; see [Running processes](#running-processes).

## SSH tunneling

netpair embeds an SSH client (`golang.org/x/crypto/ssh`) — it does not shell out to `ssh`.

Authentication:
- Default private keys from `~/.ssh/` (`id_ed25519`, `id_rsa`, `id_ecdsa`)
- Optional `--identity /path/to/key`
- Password prompt if key auth fails
- Host key verification via `~/.ssh/known_hosts`

### Reverse forward (pair side)

After `connect` exposes a service on host localhost:

```bash
netpair ssh-tunnel alpha \
  --remote user@server.example.com \
  --bind-name mysvc \
  --bind-port 8080 \
  --local-host 127.0.0.1 \
  --local-port 8080
```

Equivalent to SSH: `-R "mysvc:8080:127.0.0.1:8080"`

### Local forward (consumer side)

```bash
netpair ssh-remote \
  --remote user@server.example.com \
  --bind-name mysvc \
  --bind-port 8080 \
  --local-port 8080 \
  -- curl http://127.0.0.1:8080
```

Without `-- <argv...>`, the command holds the tunnel open until interrupted.

## Environment

Processes inside a pair inherit the environment of the terminal that ran `netpair create` / `netpair run` (DISPLAY, WAYLAND_DISPLAY, DBUS_SESSION_BUS_ADDRESS, XDG_RUNTIME_DIR, etc.).

## State

Pair metadata lives under `$XDG_RUNTIME_DIR/netpair/` (typically `/run/user/<uid>/netpair/`):

| File | Contents |
|------|----------|
| `<name>.json` | Pair state (pasta and manager PIDs — in-pair and host — and socket path) |
| `<name>.sock` | Manager UNIX socket |
| `<name>.log` | pasta log + `netpair-manager` messages + output of `--daemon` runs (useful for debugging) |
| `<name>.resolv.conf` | Corrected resolver written at `create` time |

`netpair create` truncates `<name>.log`. Attached `netpair run` writes to the caller's stdout/stderr; only `--daemon` processes inherit the log file, so their output does not interleave with the host terminal — use `netpair --verbose create` to watch startup live, or `tail -f <name>.log` for daemon output.

A running pair keeps the `netpair-manager` process it was created with: package upgrades replace the binary but not the running manager. After upgrading netpair, recreate existing pairs (`netpair destroy <name> && netpair create <name>`); `netpair doctor` flags pairs whose manager is older than the installed netpair.

## Isolation

pasta is started with no port forwarding in either direction (`-t none -T none -u none -U none`), so pair services are **not** reachable from the host until you run `netpair connect`. Outbound traffic (internet access for processes inside the pair) still works, spliced through pasta.

DNS inside a pair: pasta exposes the host's `/etc/resolv.conf` to the namespace. On systemd hosts that file points at the 127.0.0.53 stub of systemd-resolved, which has no listener inside the pair — so `netpair create` writes a corrected copy (real upstream nameservers, kept in `<name>.resolv.conf`) and the manager bind-mounts it over `/etc/resolv.conf` *inside the pair's mount namespace* only. The host file is never touched.

## Architecture

```
Host                         Pair namespace (pasta)
┌──────────────┐            ┌─────────────────────┐
│ netpair CLI  │──unix───►  │ netpair-manager     │
│ connect      │──relay──►  │  └─ your processes  │
│ ssh-tunnel   │            └─────────────────────┘
└──────────────┘
```

- **`connect`** relays TCP via the manager Unix socket (no pasta port reload).
- **`ssh-tunnel` / `ssh-remote`** use standard SSH `-R` / `-L` protocol in-process.

## Ubuntu 24.04: unprivileged user namespaces & AppArmor

Ubuntu 24.04 sets `kernel.apparmor_restrict_unprivileged_userns=1`, which forbids unconfined processes from creating user namespaces. pasta needs one, so without a profile it fails with errors like:

```
Couldn't write to /proc/self/uid_map: Operation not permitted
Couldn't configure user mappings
clone: Operation not permitted
```

(`netpair create` detects this and prints the remedy.) The fix is the bundled AppArmor profile, which the `.deb` installs as `/etc/apparmor.d/usr.bin.pasta` and reloads on installation. When it is in place, re-check with:

```bash
sudo apparmor_parser -r /etc/apparmor.d/usr.bin.pasta   # if not reloaded yet
netpair doctor
```

The profile must grant userns creation (`userns,`). Once a pasta profile is **loaded**, it decides for itself whether pasta may create a user namespace — the sysctls only apply to unconfined programs. So a stale profile (for example from netpair ≤ 0.1.1, which lacked the `userns` rule) fails with:

```
Couldn't create user namespace: Permission denied
```

even when both restriction sysctls are `0`. Reload the profile after upgrading netpair; `netpair doctor` warns when the installed profile lacks the rule.

An alternative (less secure, system-wide) is `sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0` — but a loaded pasta profile still needs `userns,`.

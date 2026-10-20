# netpair

Run process pairs in isolated network namespaces with Internet access, local port relay, and native SSH port forwarding.

Each **pair** gets its own network stack via [pasta](https://passt.top/) (from the `passt` package). A service and any other process (browser, shell command, etc.) inside the same pair share that namespace, so both can use `127.0.0.1:8080` independently of other pairs.

## Requirements

- Linux with unprivileged user namespaces enabled
- [passt](https://passt.top/) — provides `pasta` (`apt install passt`)
- [iproute2](https://wiki.linuxfoundation.org/networking/iproute2) — provides `ss` for automatic port discovery in `connect`
- Go 1.26+ (to build from source)

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

**Go version:** `go.mod` requires Go 1.26. The `golang-go` meta package on Noble still points to Go 1.22, which is too old. Install `golang-1.26-go` and put it on `PATH` before building:

```bash
export PATH=/usr/lib/go-1.26/bin:$PATH
/usr/lib/go-1.26/bin/go version   # should show go1.26.x
./package/ubuntu/build-deb.sh
```

Install the resulting package from the parent directory:

```bash
sudo apt install ../netpair_0.1.0-1_*.deb
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

The PKGBUILD clones tag `v0.1.0` into `src/netpair`. See [`package/aur/README.md`](package/aur/README.md).

## Quick start

```bash
# Create an isolated pair
netpair create alpha

# Run any process inside the namespace
netpair run alpha -- python3 -m http.server 8080

# Expose namespace ports on the host (blocking)
netpair connect alpha --port 8080

# In another terminal on the host
curl http://127.0.0.1:8080

# List / destroy
netpair ps alpha
netpair destroy alpha
```

Everything after `--` is passed directly to `exec` — no shell, no special-casing.

## Commands

| Command | Description |
|---------|-------------|
| `create <name>` | Start pasta + manager for a new pair |
| `destroy <name>` | Tear down a pair |
| `list` | List pairs |
| `status <name>` | Show pair state (JSON) |
| `run <name> -- <argv...>` | Run any process inside the pair |
| `kill <name> --id <id>` | Kill a tracked process |
| `ps <name>` | List managed processes |
| `connect <name>` | Auto-discover listening ports and forward to host |
| `connect <name> --port 8080` | Forward ns:8080 → host:8080 |
| `connect <name> --port 8080:19080` | Forward ns:8080 → host:19080 |
| `ssh-tunnel <name> --remote user@host ...` | SSH `-R` (reverse forward) |
| `ssh-remote --remote user@host ...` | SSH `-L` (local forward), optional `-- <argv...>` |

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

Pair metadata lives under `$XDG_RUNTIME_DIR/netpair/` (typically `/run/user/<uid>/netpair/`).

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

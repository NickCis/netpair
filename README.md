# netpair

Run process pairs in isolated network namespaces with Internet access, local port relay, and native SSH port forwarding.

Each **pair** gets its own network stack via [pasta](https://passt.top/) (from the `passt` package). A service and any other process (browser, shell command, etc.) inside the same pair share that namespace, so both can use `127.0.0.1:8080` independently of other pairs.

## Requirements

- Linux with unprivileged user namespaces enabled
- [passt](https://passt.top/) — provides `pasta` (`apt install passt`)
- Go 1.22+ (to build from source)
- Optional: `ss` (iproute2) for automatic port discovery in `connect`

## Build

```bash
make build
# produces ./netpair and ./netpair-manager in the project root
```

Both binaries must be on `PATH` together, or `netpair-manager` must sit next to `netpair`.

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

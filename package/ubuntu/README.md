# Ubuntu / Debian packaging for netpair

## Dependencies

| Type | Package | Purpose |
|------|---------|---------|
| Runtime | `passt` | Provides `pasta` for isolated network namespaces |
| Runtime | `iproute2` | Provides `ss` for `netpair connect` auto-discovery |
| Build | `golang-go` (>= 1.22) | Compile netpair and netpair-manager |
| Build | `debhelper` | Debian packaging helpers |

Install build tools on Ubuntu:

```bash
sudo apt install devscripts debhelper golang-go passt iproute2
```

## Build a .deb

From the repository root:

```bash
chmod +x package/ubuntu/build-deb.sh
./package/ubuntu/build-deb.sh
```

This symlinks `package/ubuntu/debian` → `debian`, runs `dpkg-buildpackage`, and removes the symlink afterward.

The binary packages appear in the parent directory (`../netpair_*.deb`).

## Install

```bash
sudo apt install ../netpair_0.1.0-1_*.deb
```

Or after building:

```bash
sudo dpkg -i ../netpair_0.1.0-1_*.deb
sudo apt-get install -f
```

## Files installed

- `/usr/bin/netpair`
- `/usr/bin/netpair-manager`
- `/usr/share/doc/netpair/README.md`

## Notes

- Both binaries must be on `PATH`; the Debian package installs both to `/usr/bin`.
- Unprivileged user namespaces must be enabled on the host.

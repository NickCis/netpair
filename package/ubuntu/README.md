# Ubuntu / Debian packaging for netpair

## Dependencies

| Type | Package | Purpose |
|------|---------|---------|
| Runtime | `passt` | Provides `pasta` for isolated network namespaces |
| Runtime | `iproute2` | Provides `ss` for `netpair connect` auto-discovery |
| Build | `golang-1.26-go` (>= 1.26) | Compile netpair and netpair-manager |
| Build | `debhelper` | Debian packaging helpers |

Install build tools on Ubuntu:

```bash
sudo apt install devscripts debhelper golang-1.26-go passt iproute2
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
sudo apt install ../netpair_0.1.5-1_*.deb
```

Or after building:

```bash
sudo dpkg -i ../netpair_0.1.5-1_*.deb
sudo apt-get install -f
```

## Files installed

- `/usr/bin/netpair`
- `/usr/bin/netpair-manager`
- `/etc/apparmor.d/usr.bin.pasta` — pasta AppArmor profile (conffile)
- `/usr/share/doc/netpair/README.md`

## Releases

Ubuntu and AUR packages must always be released together with the same
version: update `debian/changelog` **and** `package/aur/PKGBUILD` +
`.SRCINFO`, then run `make check-versions` from the repository root. See
[`../../AGENTS.md`](../../AGENTS.md).

## Notes

- Both binaries must be on `PATH`; the Debian package installs both to `/usr/bin`.
- Unprivileged user namespaces must be enabled on the host. On **Ubuntu 24.04+**
  they are restricted by AppArmor (`kernel.apparmor_restrict_unprivileged_userns=1`);
  pasta needs the shipped AppArmor profile to start a pair namespace. The profile
  is loaded automatically on package install (and at boot via `apparmor.service`).
  It must grant `userns,`: once a pasta profile is loaded it decides on its own
  whether pasta may create user namespaces, regardless of the sysctl. Upgrade
  from netpair 0.1.1 (whose profile lacked that rule) reloads the fixed profile.
  Verify the environment after installation with `netpair doctor`.

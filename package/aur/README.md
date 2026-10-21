# AUR packaging for netpair

## Dependencies

| Type | Package | Purpose |
|------|---------|---------|
| Runtime | `passt` | Provides `pasta` for isolated network namespaces |
| Runtime (optional) | `iproute2` | Provides `ss` for `netpair connect` auto-discovery |
| Build | `go` | Compile netpair and netpair-manager |

## Build locally

From this directory:

```bash
makepkg -si
```

`makepkg` clones the git tag `v${pkgver}` from the remote, so the tag must be
pushed first (see [Releasing](#releasing)).

## Releasing

The version must match the Ubuntu package; see [`../../AGENTS.md`](../../AGENTS.md)
for the full checklist.

1. Update the Ubuntu version in `package/ubuntu/debian/changelog` (source of
   truth), then set `pkgver`/`pkgrel` in `PKGBUILD`.
2. Regenerate the AUR metadata:

   ```bash
   makepkg --printsrcinfo > .SRCINFO
   ```

   Without `makepkg`, edit `.SRCINFO` by hand so `pkgver`, `pkgrel`, and the
   `#tag=vX.Y.Z` source match `PKGBUILD`.
3. From the repository root, `make check-versions` must pass.
4. Commit, then tag and push the release the package points at:

   ```bash
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```

5. Build and check the package with `makepkg -f` (or `makepkg -si`).
6. Copy `PKGBUILD` and `.SRCINFO` to the AUR package repository and push.

## Notes

- Both `netpair` and `netpair-manager` are installed to `/usr/bin/`.
- Unprivileged user namespaces must be enabled on the host (kernel config / sysctl).

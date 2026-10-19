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

Or publish to AUR:

1. Copy `PKGBUILD` and `.SRCINFO` to your AUR package directory.
2. Update `pkgver` / source tag when releasing.
3. Run `makepkg --printsrcinfo > .SRCINFO` after PKGBUILD changes.
4. Submit with `makepkg -src` and push to AUR.

## Notes

- Both `netpair` and `netpair-manager` are installed to `/usr/bin/`.
- Unprivileged user namespaces must be enabled on the host (kernel config / sysctl).

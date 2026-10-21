# Agent guidelines

## Releases: always update every package

netpair is packaged for **Ubuntu/Debian** and **Arch Linux (AUR)**. Any change
that affects a release — code, version, install layout, dependencies, or
release docs — MUST update **both** packages in the same change. Never bump
one and leave the other behind.

The version at the top of `package/ubuntu/debian/changelog` is the source of
truth: `netpair (X.Y.Z-N)` means upstream version `X.Y.Z` and package release
`N` (`pkgrel=N` for Arch).

1. **Ubuntu/Debian** (`package/ubuntu/`):
   - Add a new entry at the top of `debian/changelog` with the new version
     (repeat the version for packaging-only changes, incrementing `N`).
   - Update the version references in `README.md` and `package/ubuntu/README.md`.
   - Build the package: `./package/ubuntu/build-deb.sh`.

2. **Arch/AUR** (`package/aur/`):
   - Set `pkgver=X.Y.Z` and `pkgrel=N` in `PKGBUILD`.
   - Regenerate `.SRCINFO` with `makepkg --printsrcinfo > .SRCINFO` on an Arch
     machine (without `makepkg`, edit it by hand so it matches `PKGBUILD`
     exactly, including `pkgver`, `pkgrel`, and the `#tag=vX.Y.Z` source).
   - Build/check with `makepkg -f` (or `makepkg -si`) before publishing.

3. **Tag the release**: the AUR package builds from the git tag `vX.Y.Z` on
   the remote. After committing the release, create and push it:
   `git tag vX.Y.Z && git push origin vX.Y.Z`. Without the tag `makepkg`
   fails; with a stale tag it silently builds an old version (the v0.1.0
   problem).

4. **Verify** from the repository root:

   ```bash
   make check-versions
   ```

   It compares the Debian changelog, `PKGBUILD`, `.SRCINFO`, and the version
   references in the READMEs, and warns when the tag is missing.

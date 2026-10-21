#!/usr/bin/env bash
# Check that the Ubuntu/Debian and AUR packages carry the same version and
# that the docs point at it. The newest debian/changelog entry is the source
# of truth: "netpair (X.Y.Z-N)" means upstream X.Y.Z with pkgrel N.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
changelog="$root/package/ubuntu/debian/changelog"
pkgbuild="$root/package/aur/PKGBUILD"
srcinfo="$root/package/aur/.SRCINFO"

deb_ver="$(sed -n 's/^netpair (\([^)]*\)).*/\1/p' "$changelog" | head -n1)"
if [[ -z "$deb_ver" ]]; then
  echo "error: cannot parse the version from $changelog" >&2
  exit 1
fi
upstream="${deb_ver%-*}"
pkgrel="${deb_ver##*-}"

aur_pkgver="$(sed -n 's/^pkgver=//p' "$pkgbuild")"
aur_pkgrel="$(sed -n 's/^pkgrel=//p' "$pkgbuild")"
src_pkgver="$(sed -n 's/^\tpkgver = //p' "$srcinfo")"
src_pkgrel="$(sed -n 's/^\tpkgrel = //p' "$srcinfo")"
src_tag="$(sed -n 's/.*#tag=v\([0-9][0-9.]*\).*/\1/p' "$srcinfo" | head -n1)"

fail=0
check() { # description want got
  if [[ "$2" != "$3" ]]; then
    printf 'error: %s is %q, want %q\n' "$1" "$3" "$2" >&2
    fail=1
  fi
}

check "AUR PKGBUILD pkgver" "$upstream" "$aur_pkgver"
check "AUR PKGBUILD pkgrel" "$pkgrel" "$aur_pkgrel"
check "AUR .SRCINFO pkgver" "$upstream" "$src_pkgver"
check "AUR .SRCINFO pkgrel" "$pkgrel" "$src_pkgrel"
check "AUR .SRCINFO source tag" "$upstream" "$src_tag"

# The source tag must follow pkgver instead of being hardcoded (which is how
# the AUR package kept building v0.1.0 after later releases).
if ! grep -qF '#tag=v${pkgver}' "$pkgbuild"; then
  echo 'error: AUR PKGBUILD source must use #tag=v${pkgver}' >&2
  fail=1
fi

# Docs must reference the current package file name.
want_deb="netpair_${upstream}-${pkgrel}_"
stale_docs="$(grep -rhoE 'netpair_[0-9]+\.[0-9]+\.[0-9]+-[0-9]+_' \
  "$root"/README.md "$root"/package/*/README.md 2>/dev/null |
  sort -u | grep -vx "$want_deb" || true)"
if [[ -n "$stale_docs" ]]; then
  printf 'error: stale package version in docs:\n%s\n  want %s...\n' \
    "$stale_docs" "$want_deb" >&2
  fail=1
fi

# Tag references such as the PKGBUILD's "clones tag `vX.Y.Z`" note.
stale_tags="$(grep -rhoE 'tag `v[0-9]+\.[0-9]+\.[0-9]+`' \
  "$root"/README.md "$root"/package/*/README.md 2>/dev/null |
  sort -u | grep -vxF "tag \`v${upstream}\`" || true)"
if [[ -n "$stale_tags" ]]; then
  printf 'error: stale release tag in docs:\n%s\n  want tag `v%s`\n' \
    "$stale_tags" "$upstream" >&2
  fail=1
fi

if ! git -C "$root" rev-parse -q --verify "refs/tags/v${upstream}" >/dev/null; then
  echo "warning: git tag v${upstream} does not exist yet; the AUR package cannot build until it is created and pushed" >&2
fi

if ((fail)); then
  exit 1
fi
echo "ok: netpair ${upstream}-${pkgrel} is in sync (Ubuntu, AUR, docs)"

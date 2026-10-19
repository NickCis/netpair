#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
debian_src="$root/package/ubuntu/debian"
debian_link="$root/debian"

cleanup() {
  if [[ -L "$debian_link" ]]; then
    rm -f "$debian_link"
  fi
}
trap cleanup EXIT

if [[ -e "$debian_link" && ! -L "$debian_link" ]]; then
  echo "error: $debian_link exists and is not a symlink" >&2
  exit 1
fi

ln -sfn "$debian_src" "$debian_link"

cd "$root"
dpkg-buildpackage -us -uc -b

echo
echo "Packages written to $(dirname "$root")/"

#!/usr/bin/env bash
# Publishes a release built by `make dist` into the directory nginx serves
# as https://pantechdynamics.com/cli/ (and /install), on the website server:
#
#   scripts/publish-release.sh dist [/srv/pantech-downloads/cli]
#
# The release workflow runs this on a runner on that server. It:
#   - checks every archive against SHA256SUMS before anything moves;
#   - refuses to replace a published version: nginx lets those be cached for
#     a year, so a version, once out, never changes;
#   - moves the version into place in one step, then the installer, then
#     latest.txt, so no one is ever offered a release that is half there;
#   - publishes a pre-release (v0.2.0-rc.1) without making it the latest.

set -euo pipefail

fail() {
  printf 'publish: %s\n' "$*" >&2
  exit 1
}

dist="${1:?usage: publish-release.sh <dist dir> [destination]}"
dest="${2:-/srv/pantech-downloads/cli}"

[ -f "$dist/latest.txt" ] || fail "$dist/latest.txt is missing: run make dist first"
version=$(tr -d '[:space:]' <"$dist/latest.txt")
case "$version" in
  v[0-9]*) ;;
  *) fail "\"$version\" is not a release version (v1.2.3)" ;;
esac
[ -d "$dist/$version" ] || fail "$dist/$version is missing"
[ -f "$dist/install" ] || fail "$dist/install is missing"
[ -d "$dest" ] && [ -w "$dest" ] || fail "$dest does not exist or is not writable"
[ -e "$dest/$version" ] && fail "$version is already published: release a new version instead"

# Every archive matches its checksum.
(
  cd "$dist/$version"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum --check --quiet SHA256SUMS
  else
    shasum -a 256 --check --quiet SHA256SUMS
  fi
) || fail "checksums do not match"

# The version, all at once: built beside it, then renamed into place.
incoming=$(mktemp -d "$dest/.incoming.XXXXXX")
trap 'rm -rf "$incoming"' EXIT
cp "$dist/$version"/* "$incoming/"
chmod 644 "$incoming"/*
chmod 755 "$incoming"
mv "$incoming" "$dest/$version"
trap - EXIT
echo "published $dest/$version"

case "$version" in
  *-*)
    echo "$version is a pre-release: latest.txt and the installer are unchanged"
    exit 0
    ;;
esac

# Then the installer, and last the pointer to the newest version.
install -m 644 "$dist/install" "$dest/.install.tmp"
mv "$dest/.install.tmp" "$dest/install"
printf '%s\n' "$version" >"$dest/.latest.tmp"
chmod 644 "$dest/.latest.tmp"
mv "$dest/.latest.tmp" "$dest/latest.txt"
echo "latest is now $version"

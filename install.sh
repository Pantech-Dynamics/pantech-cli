#!/usr/bin/env bash
# Installs the Pantech CLI:
#
#   curl -fsSL https://pantechdynamics.com/install | bash
#   curl -fsSL https://pantechdynamics.com/install | bash -s -- v0.2.0   # a given version
#
# It downloads the binary for this machine, checks it against the release's
# SHA256SUMS, puts it in ~/.pantech/bin and adds that to your PATH.
#
#   PANTECH_INSTALL          where to install (default: ~/.pantech)
#   PANTECH_VERSION          version to install (default: the latest)
#   PANTECH_NO_MODIFY_PATH   set to leave your shell's startup file alone
#   PANTECH_DOWNLOAD_URL     where releases are served from
#
# Everything runs inside main(), so a download cut off half way runs nothing.

set -euo pipefail

# Releases: $DOWNLOAD_URL/latest.txt names the newest version, and
# $DOWNLOAD_URL/<version>/ holds pantech_<os>_<arch>.tar.gz and SHA256SUMS.
DOWNLOAD_URL="${PANTECH_DOWNLOAD_URL:-https://pantechdynamics.com/cli}"

say() { printf '%s\n' "$*" >&2; }
fail() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

detect_platform() {
  local os arch
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) fail "the Pantech CLI runs on macOS and Linux, not $(uname -s)" ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) fail "no build for $(uname -m)" ;;
  esac
  # An Intel shell under Rosetta on Apple silicon: install the native build.
  if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
    arch=arm64
  fi
  printf '%s_%s' "$os" "$arch"
}

download() {
  if command -v curl >/dev/null 2>&1; then
    curl --fail --silent --show-error --location --retry 2 --output "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget --quiet --output-document="$2" "$1"
  else
    fail "curl or wget is needed to download the CLI"
  fi
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
  else
    fail "sha256sum or shasum is needed to check the download"
  fi
}

# add_to_path writes the PATH line to the shell's startup file, once.
add_to_path() {
  local bin=$1 rc line
  case "$(basename "${SHELL:-}")" in
    zsh) rc="${ZDOTDIR:-$HOME}/.zshrc" ;;
    bash)
      rc="$HOME/.bashrc"
      [ "$(uname -s)" = Darwin ] && rc="$HOME/.bash_profile"
      ;;
    fish) rc="$HOME/.config/fish/config.fish" ;;
    *) rc="" ;;
  esac
  if [ "$(basename "${SHELL:-}")" = fish ]; then
    line="fish_add_path \"$bin\""
  else
    line="export PATH=\"$bin:\$PATH\""
  fi

  if [ -z "$rc" ]; then
    say "Add the CLI to your PATH:"
    say "  $line"
    return
  fi
  if [ -f "$rc" ] && grep -Fq "$bin" "$rc"; then
    return
  fi
  mkdir -p "$(dirname "$rc")"
  printf '\n# Pantech CLI\n%s\n' "$line" >>"$rc"
  say "Added $bin to your PATH in $rc. Open a new terminal, or run:"
  say "  $line"
}

main() {
  local platform version install_dir bin tmp archive
  platform=$(detect_platform)
  version="${1:-${PANTECH_VERSION:-}}"
  install_dir="${PANTECH_INSTALL:-$HOME/.pantech}"
  bin="$install_dir/bin"

  tmp=$(mktemp -d)
  # Expanded now: by the time the trap runs, main's locals are gone.
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT

  if [ -z "$version" ]; then
    download "$DOWNLOAD_URL/latest.txt" "$tmp/latest.txt" || fail "could not find the latest version at $DOWNLOAD_URL"
    version=$(tr -d '[:space:]' <"$tmp/latest.txt")
  fi
  case "$version" in v*) ;; *) version="v$version" ;; esac

  archive="pantech_${platform}.tar.gz"
  say "Installing pantech $version for ${platform}…"
  download "$DOWNLOAD_URL/$version/$archive" "$tmp/$archive" || fail "no $archive for $version at $DOWNLOAD_URL"
  download "$DOWNLOAD_URL/$version/SHA256SUMS" "$tmp/SHA256SUMS" || fail "no SHA256SUMS for $version"

  local want got
  want=$(grep " $archive\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)
  got=$(sha256 "$tmp/$archive")
  [ -n "$want" ] || fail "SHA256SUMS does not list $archive"
  [ "$want" = "$got" ] || fail "the download does not match its checksum: not installing it"

  tar -xzf "$tmp/$archive" -C "$tmp"
  mkdir -p "$bin"
  install -m 0755 "$tmp/pantech" "$bin/pantech"
  say "Installed $("$bin/pantech" --version) to $bin/pantech"

  case ":$PATH:" in
    *":$bin:"*) ;;
    *)
      if [ -n "${PANTECH_NO_MODIFY_PATH:-}" ]; then
        say "Add $bin to your PATH to run pantech."
      else
        add_to_path "$bin"
      fi
      ;;
  esac
  say ""
  say "Next: pantech auth login"
}

main "$@"

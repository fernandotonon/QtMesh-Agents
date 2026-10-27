#!/usr/bin/env bash
# Usage: curl -fsSL https://raw.githubusercontent.com/fernandotonon/QtMesh-Agents/main/scripts/install.sh | bash
# Or: bash scripts/install.sh --binary ./bin/qma
set -euo pipefail
repo=fernandotonon/QtMesh-Agents
version=${QMA_VERSION:-latest}
dest=${QMA_INSTALL_DIR:-"$HOME/.local/bin"}
binary=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --binary|--version|--dir)
      [ "$#" -ge 2 ] || { echo "missing value for $1" >&2; exit 1; }
      case "$1" in --binary) binary=$2;; --version) version=$2;; --dir) dest=$2;; esac
      shift 2;;
    --help) echo 'Usage: install.sh [--binary PATH] [--version vX.Y.Z] [--dir DIR]'; exit 0;;
    *) echo "unknown option: $1" >&2; exit 1;;
  esac
done
if [ -z "$binary" ]; then
  case "$(uname -s)" in Linux) platform=linux;; Darwin) platform=darwin;; *) echo 'Use a release zip on Windows.' >&2; exit 1;; esac
  case "$(uname -m)" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo 'Unsupported architecture' >&2; exit 1;; esac
  command -v curl >/dev/null
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  if [ "$version" = latest ]; then
    base="https://github.com/$repo/releases/latest/download"
    # The release redirect supplies the tag without requiring a JSON parser.
    url=$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest")
    version=${url##*/}
  else
    base="https://github.com/$repo/releases/download/$version"
  fi
  archive="qtmesh-agents_${version#v}_${platform}_${arch}.tar.gz"
  curl -fsSL "$base/$archive" -o "$tmp/$archive"
  curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"
  (cd "$tmp"
   awk -v file="$archive" '$2 == file { print; found=1 } END { if (!found) exit 1 }' checksums.txt > selected-checksum.txt
   if command -v sha256sum >/dev/null; then sha256sum -c selected-checksum.txt; else shasum -a 256 -c selected-checksum.txt; fi
   tar -xzf "$archive" qma)
  binary="$tmp/qma"
fi
[ -f "$binary" ] || { echo "Binary not found: $binary" >&2; exit 1; }
mkdir -p "$dest"
install -m 755 "$binary" "$dest/qma"
echo "Installed $dest/qma"
case ":$PATH:" in *":$dest:"*) ;; *) echo "Add to your shell profile: export PATH=\"$dest:\$PATH\"";; esac
echo 'Run qma init to get started.'

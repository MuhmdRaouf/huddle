#!/bin/sh
# Installs what `cargo zigbuild` needs to build the static Linux binaries: zig and
# cargo-zigbuild, the same releases the Dockerfile pins. Bump both files together.
set -eu

zig_version=0.16.0
zigbuild_version=0.24.1
case "$(uname -m)" in
  x86_64) arch=x86_64 sha=70e49664a74374b48b51e6f3fdfbf437f6395d42509050588bd49abe52ba3d00 ;;
  aarch64 | arm64) arch=aarch64 sha=ea4b09bfb22ec6f6c6ceac57ab63efb6b46e17ab08d21f69f3a48b38e1534f17 ;;
  *) echo "zig-musl: unsupported machine $(uname -m)" >&2; exit 1 ;;
esac

dir=${RUNNER_TEMP:?}/zig
tarball=$RUNNER_TEMP/zig.tar.xz
curl -fsSL --retry 5 --retry-all-errors -o "$tarball" \
  "https://ziglang.org/download/$zig_version/zig-$arch-linux-$zig_version.tar.xz"
echo "$sha  $tarball" | sha256sum --check -
mkdir -p "$dir"
tar -xJf "$tarball" --directory "$dir" --strip-components=1
echo "$dir" >>"${GITHUB_PATH:?}"

cargo install --locked "cargo-zigbuild@$zigbuild_version"

#!/bin/sh
# Run from the project root on Linux with Go, GNU tar and dpkg-deb.
set -eu
version=${1:?usage: packaging/release.sh VERSION}
dpkg --validate-version "$version"
mkdir -p dist
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
export CGO_ENABLED=0 SOURCE_DATE_EPOCH=0
for arch in amd64 arm64; do
    deb="$stage/deb-$arch"
    mkdir -p "$deb/DEBIAN" "$deb/usr/bin"
    cat > "$deb/DEBIAN/control" <<EOF
Package: webster
Version: $version
Architecture: $arch
Maintainer: lonetis <19536861+lonetis@users.noreply.github.com>
Section: net
Priority: optional
Depends: ca-certificates
Homepage: https://github.com/lonetis/webster
Description: Terminal connection tester for CrUX web origins
 Check HTTP reachability with a live dashboard and export per-origin results.
EOF
    flags="-s -w -X main.userDataRoot=.local/share/webster"
    GOOS=linux GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags "$flags" -o "$deb/usr/bin/webster" .
    dpkg-deb --root-owner-group -Zxz --build "$deb" "dist/webster-linux-$arch.deb"

    GOOS=darwin GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags "$flags" -o "$stage/webster" .
    tar --mtime=@0 --owner=0 --group=0 --numeric-owner -C "$stage" -cf "$stage/mac.tar" webster
    gzip -n -c "$stage/mac.tar" > "dist/webster-macos-$arch.tar.gz"
done

(cd dist && sha256sum webster-linux-amd64.deb webster-linux-arm64.deb webster-macos-amd64.tar.gz webster-macos-arm64.tar.gz > SHA256SUMS)

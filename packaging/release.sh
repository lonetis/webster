#!/bin/sh
# Run VERSION once on macOS and once on Linux, collecting both outputs in dist/.
# Then run packaging/release.sh checksums in the combined checkout.
set -eu
version=${1:?usage: packaging/release.sh VERSION or checksums}
if [ "$version" = checksums ]; then
    cd dist
    shasum -a 256 webster-linux-amd64.deb webster-linux-arm64.deb webster-macos-amd64.tar.gz webster-macos-arm64.tar.gz > SHA256SUMS
    exit
fi
mkdir -p dist
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
flags="-s -w -X main.userDataRoot=.local/share/webster"
export SOURCE_DATE_EPOCH=0
case $(uname -s) in
    Darwin)
        # ZDNS's certificate dependency needs cgo and the macOS SDK on Apple Silicon.
        # Match the Go toolchain's macOS deployment target with the external linker.
        export MACOSX_DEPLOYMENT_TARGET=13.0
        export CGO_CFLAGS="-O2 -mmacosx-version-min=13.0"
        export CGO_LDFLAGS="-O2 -mmacosx-version-min=13.0"
        for arch in amd64 arm64; do
            CGO_ENABLED=1 GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags "$flags" -o "$stage/webster" .
            TZ=UTC touch -t 197001010000 "$stage/webster"
            COPYFILE_DISABLE=1 tar --format ustar --uid 0 --gid 0 --uname root --gname wheel -C "$stage" -cf "$stage/mac.tar" webster
            gzip -n -c "$stage/mac.tar" > "dist/webster-macos-$arch.tar.gz"
        done
        exit
        ;;
    Linux) dpkg --validate-version "$version" ;;
    *) echo "Build packages on Linux or macOS" >&2; exit 1 ;;
esac
export CGO_ENABLED=0
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
 Check HTTP reachability with a live dashboard and optional ZDNS resolution.
EOF
    GOOS=linux GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags "$flags" -o "$deb/usr/bin/webster" .
    dpkg-deb --root-owner-group -Zxz --build "$deb" "dist/webster-linux-$arch.deb"
done

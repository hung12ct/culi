#!/bin/sh
# Build the release archives for every supported platform into dist/.
#
#   scripts/dist.sh v0.7.1
#
# culi is pure Go (modernc.org/sqlite, no CGO), so one machine cross-compiles
# every target. Archive names carry no version, which lets install.sh fetch
# releases/latest/download/culi_<os>_<arch>.tar.gz without an API call.
set -eu

version=${1:?usage: scripts/dist.sh <version>}
out=dist
ldflags="-s -w -X github.com/hung12ct/culi/internal/cli.version=$version"

rm -rf "$out"
mkdir -p "$out"

for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64; do
	os=${target%/*}
	arch=${target#*/}
	name="culi_${os}_${arch}"
	stage="$out/$name"
	bin=culi
	[ "$os" = windows ] && bin=culi.exe

	mkdir -p "$stage"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
		go build -trimpath -ldflags "$ldflags" -o "$stage/$bin" ./cmd/culi
	cp LICENSE README.md "$stage/"

	if [ "$os" = windows ]; then
		(cd "$stage" && zip -q "../$name.zip" "$bin" LICENSE README.md)
	else
		tar -C "$stage" -czf "$out/$name.tar.gz" "$bin" LICENSE README.md
	fi
	rm -rf "$stage"
done

cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum culi_* >checksums.txt
else
	shasum -a 256 culi_* >checksums.txt
fi
echo "dist: built $(ls culi_* | wc -l | tr -d ' ') archives for $version"

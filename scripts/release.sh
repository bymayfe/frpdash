#!/bin/sh
# Publishes a GitHub release with the files install.sh downloads:
# frpdash-linux-amd64, frpdash-linux-arm64 and checksums.txt.
#
#   make release VERSION=v0.1.0
#
# Needs a clean main branch that is pushed, and the GitHub CLI (gh) logged in.
set -eu

version=${1:-}
case "$version" in
v[0-9]*.[0-9]*.[0-9]*) ;;
*)
	echo "usage: make release VERSION=vX.Y.Z" >&2
	exit 2
	;;
esac

[ "$(git rev-parse --abbrev-ref HEAD)" = "main" ] || { echo "release: switch to main first" >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "release: commit or stash your changes first" >&2; exit 1; }
git fetch -q origin main
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || { echo "release: push main first" >&2; exit 1; }
if git rev-parse -q --verify "refs/tags/$version" >/dev/null || gh release view "$version" >/dev/null 2>&1; then
	echo "release: $version already exists" >&2
	exit 1
fi

echo "==> Building $version"
make -s clean
make -s build-linux VERSION="$version"
(cd bin && sha256sum frpdash-linux-amd64 frpdash-linux-arm64 >checksums.txt)

echo "==> Publishing $version"
gh release create "$version" bin/frpdash-linux-amd64 bin/frpdash-linux-arm64 bin/checksums.txt \
	--target main --title "frpdash $version" --generate-notes

echo "==> Done: install.sh now installs $version"

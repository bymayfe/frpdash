#!/bin/sh
# Downloads the Tailwind CSS standalone CLI (no Node needed) at a pinned
# version and checks it against a pinned SHA-256.
#   scripts/fetch-tailwind.sh <version> <output path>
set -eu

version=$1
out=$2

case "$(uname -s)-$(uname -m)" in
Linux-x86_64)
	asset=tailwindcss-linux-x64
	sum=e47695cee3f0d90623a91f3939b08bdee610eb5ad99340adff9d4145b284d6cc
	;;
Linux-aarch64 | Linux-arm64)
	asset=tailwindcss-linux-arm64
	sum=ffcd0d21dbd7c605dc12743ff0c0b47debc963c86b399674084c1c8f2c96ecad
	;;
*)
	echo "fetch-tailwind: no pinned build for $(uname -s)-$(uname -m)" >&2
	exit 1
	;;
esac

if [ "$version" != "v4.3.5" ]; then
	echo "fetch-tailwind: checksums are pinned for v4.3.5; update them in $0 for $version" >&2
	exit 1
fi

mkdir -p "$(dirname "$out")"
tmp="$out.download"
curl -fsSL -o "$tmp" "https://github.com/tailwindlabs/tailwindcss/releases/download/$version/$asset"
echo "$sum  $tmp" | sha256sum -c --quiet -
chmod 0755 "$tmp"
mv "$tmp" "$out"
echo "fetched $asset $version"

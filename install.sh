#!/bin/sh
# frpdash installer for the server (the VDS that runs frps).
#
# Install or update (run it again to update; data and settings are kept):
#   curl -fsSL https://raw.githubusercontent.com/bymayfe/frpdash/main/install.sh | sudo sh
#
# Options (after `sh -s --`):
#   --version vX.Y.Z   install that release instead of the latest
#   --uninstall        remove frpdash (server and/or agent) but keep its data
#   --purge            remove frpdash and all its data (database, certificates, settings)
#
#   curl -fsSL https://raw.githubusercontent.com/bymayfe/frpdash/main/install.sh | sudo sh -s -- --uninstall
#
# Clients (the frpc side) are not installed with this script: add them in the
# panel with "Add client", which gives a one-line command for each machine.
set -eu

REPO="bymayfe/frpdash"
BIN="/usr/local/bin/frpdash"
SERVER_DATA="/var/lib/frpdash"

say() { printf 'frpdash: %s\n' "$*"; }
die() {
	printf 'frpdash: %s\n' "$*" >&2
	exit 1
}

main() {
	version=""
	action="install"
	while [ $# -gt 0 ]; do
		case "$1" in
		--version)
			[ $# -ge 2 ] || die "--version needs a value, e.g. --version v0.1.0"
			version=$2
			shift 2
			;;
		--uninstall) action="uninstall"; shift ;;
		--purge) action="purge"; shift ;;
		-h | --help)
			sed -n '2,15p' "$0" 2>/dev/null || true
			exit 0
			;;
		*) die "unknown option: $1" ;;
		esac
	done

	[ "$(id -u)" -eq 0 ] || die "run as root: curl ... | sudo sh"
	[ "$(uname -s)" = "Linux" ] || die "only Linux is supported"
	command -v systemctl >/dev/null || die "systemd is required"

	case "$action" in
	install) do_install ;;
	uninstall) do_uninstall ;;
	purge) do_uninstall && do_purge ;;
	esac
}

# Not named "install": the function calls install(1).
do_install() {
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 other=arm64 ;;
	aarch64 | arm64) arch=arm64 other=amd64 ;;
	*) die "unsupported architecture $(uname -m) (amd64 and arm64 are supported)" ;;
	esac
	command -v curl >/dev/null || die "curl is required"
	command -v sha256sum >/dev/null || die "sha256sum is required"

	if [ -n "$version" ]; then
		base="https://github.com/$REPO/releases/download/$version"
	else
		base="https://github.com/$REPO/releases/latest/download"
	fi

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT

	say "downloading ${version:-the latest release} (linux/$arch, plus linux/$other for clients)"
	for f in checksums.txt "frpdash-linux-$arch" "frpdash-linux-$other"; do
		curl -fsSL -o "$tmp/$f" "$base/$f" || die "download failed: $base/$f"
	done

	say "verifying checksums"
	(cd "$tmp" && grep -E " frpdash-linux-(amd64|arm64)\$" checksums.txt | sha256sum -c --quiet -) ||
		die "checksum mismatch; nothing was installed"
	chmod 0755 "$tmp/frpdash-linux-$arch"

	# Copies itself to /usr/local/bin, installs and (re)starts frpdash-server,
	# and prints how to open the panel (with the setup code on a first install).
	"$tmp/frpdash-linux-$arch" server --install-service

	# The panel's one-line client installer serves this to clients of the
	# other architecture (e.g. a Raspberry Pi behind an amd64 VDS).
	install -D -m 0755 "$tmp/frpdash-linux-$other" "$SERVER_DATA/dist/frpdash-linux-$other"

	say "installed $("$BIN" version)"
}

do_uninstall() {
	found=""
	for unit in frpdash-server frpdash-agent; do
		if [ -f "/etc/systemd/system/$unit.service" ]; then
			say "removing $unit"
			systemctl disable --now "$unit" 2>/dev/null || true
			rm -f "/etc/systemd/system/$unit.service"
			found=1
		fi
	done
	systemctl daemon-reload
	rm -f "$BIN"
	[ -n "$found" ] || say "no frpdash service was installed"
	if [ "$action" = "uninstall" ]; then
		say "removed. Data is kept in $SERVER_DATA (server) or /var/lib/frpdash-agent and /etc/frpdash (agent);"
		say "use --purge to delete it. Firewall rules frpdash opened are left in place."
	fi
}

do_purge() {
	rm -rf "$SERVER_DATA" /var/lib/frpdash-agent /etc/frpdash
	say "all frpdash data deleted. Firewall rules frpdash opened are left in place."
}

# Everything runs from main, called on the last line, so a partly downloaded
# script does nothing.
main "$@"

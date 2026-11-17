#!/bin/sh
# Installs or updates the frpdash server on a VDS over SSH, from a checkout of
# this repository: the binaries are built here and copied over, so the VDS
# does not need Go or a copy of the source.
#
#   make deploy-server                      (asks for the VDS address)
#   make deploy-server VDS=<ssh host> [PUBLIC_HOST=<ip or name>]
#
# In VS Code: Terminal → Run Task → "Deploy server to VDS".
#
# Copies both linux builds: the one matching the VDS becomes the server, the
# other goes to /var/lib/frpdash/dist so clients of that architecture can be
# installed with the one-line command too. Re-running it updates the binary
# and restarts the service; the database and settings are kept.
set -eu

vds=${1:-}
public_host=${2:-}
if [ -z "$vds" ]; then
	# Asked interactively: `make deploy-server` or the VS Code task without VDS=.
	printf 'VDS SSH address (e.g. root@203.0.113.10, or a Host from ~/.ssh/config): '
	read -r vds
	if [ -z "$vds" ]; then
		echo "deploy-server: no address given" >&2
		exit 2
	fi
fi
case "$public_host" in
*[!A-Za-z0-9.:-]*)
	echo "deploy-server: PUBLIC_HOST must be an IP address or host name" >&2
	exit 2
	;;
esac

# One SSH connection for the whole deploy, so the password is asked once
# (ssh, scp and the install all reuse it).
ctl=$(mktemp -d /tmp/frpdash-ssh.XXXXXX)
ssh_opts="-o ControlMaster=auto -o ControlPath=$ctl/conn -o ControlPersist=120"
cleanup() {
	ssh $ssh_opts -O exit "$vds" 2>/dev/null || true
	rm -rf "$ctl"
}
trap cleanup EXIT

echo "==> Checking $vds"
case "$(ssh $ssh_opts "$vds" uname -m)" in
x86_64 | amd64) arch=amd64 other=arm64 ;;
aarch64 | arm64) arch=arm64 other=amd64 ;;
*)
	echo "deploy-server: unsupported architecture on $vds" >&2
	exit 1
	;;
esac

for f in "bin/frpdash-linux-$arch" "bin/frpdash-linux-$other"; do
	[ -f "$f" ] || { echo "deploy-server: $f is missing; run make build-linux" >&2; exit 1; }
done

echo "==> Copying frpdash (linux/$arch, plus linux/$other for clients)"
scp -q $ssh_opts "bin/frpdash-linux-$arch" "$vds:/tmp/frpdash"
scp -q $ssh_opts "bin/frpdash-linux-$other" "$vds:/tmp/frpdash-$other"

flags="--install-service"
if [ -n "$public_host" ]; then
	flags="$flags --public-host $public_host"
fi

echo "==> Installing on $vds (sudo may ask for your password if you are not root)"
# -t: a terminal for sudo. The server is installed first so it creates its
# data directory with the right permissions; dist/ goes inside it afterwards.
# root logins (common on a VDS) may not have sudo at all.
ssh -t $ssh_opts "$vds" "set -e
if [ \"\$(id -u)\" -eq 0 ]; then as_root=; else as_root=sudo; fi
\$as_root /tmp/frpdash server $flags
\$as_root install -D -m 0755 /tmp/frpdash-$other /var/lib/frpdash/dist/frpdash-linux-$other
rm -f /tmp/frpdash /tmp/frpdash-$other"

echo
echo "==> Done. Open the panel with:"
echo "    ssh -L 8090:127.0.0.1:8090 $vds     then http://localhost:8090"

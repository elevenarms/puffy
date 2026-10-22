#!/bin/sh
# get.sh — install puffy on OpenBSD amd64 with one command:
#
#   ftp -o - https://raw.githubusercontent.com/elevenarms/puffy/main/get.sh | doas sh
#
# Downloads the latest release with ftp(1), verifies it with signify(1)
# against the puffy release key below (the same scheme OpenBSD uses for
# its own sets), and runs the bundled install.sh. Needs no packages.
# Set PUFFY_RELEASE=v0.1.0 to pin a version.

set -eu

REPO=https://github.com/elevenarms/puffy/releases
TARBALL=puffy-openbsd80-amd64.tgz
PUBKEY='untrusted comment: puffy release signing key public key
RWRT3j9AugHfOIahtX9ZAjhOmAuwCTBcBwK8G6O0MQeO4HwED0EudSi8'

fail() {
	echo "puffy install: $*" >&2
	exit 1
}

[ "$(uname -s)" = OpenBSD ] || fail "this is for OpenBSD"
[ "$(uname -m)" = amd64 ] || fail "this build is for amd64"
[ "$(id -u)" -eq 0 ] || fail "run it as root: ... | doas sh"

if [ -n "${PUFFY_RELEASE:-}" ]; then
	URL="$REPO/download/$PUFFY_RELEASE"
else
	URL="$REPO/latest/download"
fi

# /usr/local is where puffy goes, so it has the room (about 1 GB while
# installing; /tmp is often too small).
WORK=$(mktemp -d /usr/local/.puffy-install.XXXXXXXX)
trap 'rm -rf "$WORK"' EXIT INT TERM
cd "$WORK"

echo "puffy: downloading $URL/$TARBALL"
printf '%s\n' "$PUBKEY" > puffy-release.pub
ftp -V -o SHA256.sig "$URL/SHA256.sig" || fail "cannot download SHA256.sig"
ftp -o "$TARBALL" "$URL/$TARBALL" || fail "cannot download $TARBALL"

echo "puffy: verifying signature"
signify -C -p puffy-release.pub -x SHA256.sig "$TARBALL" ||
	fail "signature check failed; nothing was installed"

tar xzf "$TARBALL"
rm -f "$TARBALL"
set -- puffy-*-openbsd80-amd64
[ -d "$1" ] || fail "unexpected tarball layout"
"./$1/install.sh"

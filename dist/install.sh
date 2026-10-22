#!/bin/sh
# install.sh — install puffy from the unpacked distributable into
# /usr/local (or PREFIX). Run as root: doas ./install.sh
# Needs nothing from packages: puffy, vector.so and llama-server use only
# OpenBSD base libraries. Remove with:
#   doas sh /usr/local/share/puffy/install.sh -u

set -eu

PREFIX=${PREFIX:-/usr/local}
HERE=$(cd "$(dirname "$0")" && pwd)
FILES="bin/puffy libexec/puffy/llama-server lib/puffy/vector.so
    share/puffy/puffy-kb.db share/puffy/nomic-embed-text-v1.5.Q8_0.gguf
    share/doc/puffy/README.md share/doc/puffy/SETUP.md
    share/doc/puffy/LICENSE share/doc/puffy/NOTICE
    share/doc/puffy/puffy-release.pub share/puffy/install.sh"

if [ "$(id -u)" -ne 0 ]; then
    echo "install.sh: run as root (doas $0)" >&2
    exit 1
fi

if [ "${1:-}" = -u ]; then
    for file in $FILES; do rm -f "$PREFIX/$file"; done
    rmdir "$PREFIX/libexec/puffy" "$PREFIX/lib/puffy" \
        "$PREFIX/share/puffy" "$PREFIX/share/doc/puffy" 2>/dev/null || true
    echo "puffy removed from $PREFIX (your ~/.puffy is kept)"
    exit 0
fi

if [ "$(uname -s)" != OpenBSD ] || [ "$(uname -m)" != amd64 ]; then
    echo "install.sh: this build is for OpenBSD amd64" >&2
    exit 1
fi
case "$(uname -r)" in
    8.0) ;;
    *) echo "install.sh: built on OpenBSD 8.0; on $(uname -r) it may not run" >&2 ;;
esac

if [ -f "$HERE/SHA256" ]; then
    (cd "$HERE" && sha256 -q -c SHA256) || {
        echo "install.sh: checksum mismatch; download again" >&2
        exit 1
    }
fi

for file in $FILES; do
    install -d "$PREFIX/$(dirname "$file")"
    case $file in
        bin/*|libexec/*) mode=755 ;;
        *) mode=644 ;;
    esac
    install -o root -g bin -m "$mode" "$HERE/$file" "$PREFIX/$file"
done

echo "puffy installed in $PREFIX. As your normal user, run:"
echo "    puffy            # first run walks you through setup"
echo "    puffy --doctor   # check everything"
echo "Search works offline; the model needs a TrustedRouter key."

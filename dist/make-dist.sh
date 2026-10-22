#!/bin/sh
# make-dist.sh — build the offline distributable on OpenBSD amd64.
#
# Run on an OpenBSD 8.0 amd64 build machine (the test VM works) with
# go, gmake and cmake installed, from the puffy source folder:
#
#   dist/make-dist.sh /path/to/sqlite-vector /path/to/llama.cpp data/
#
# data/ holds puffy-kb.db and nomic-embed-text-v1.5.Q8_0.gguf. Writes
# puffy-openbsd80-amd64.tgz (a fixed name, so GitHub's releases/latest
# URL finds it; the folder inside carries the version) and SHA256 in the
# current folder. Sign with: signify -S -e -s KEY -m SHA256 -x SHA256.sig

set -eu

VECTOR_SRC=$1
LLAMA_SRC=$2
DATA=$3
VERSION=$(sed -n 's/^const version = "\(.*\)"/\1/p' main.go)
NAME="puffy-$VERSION-openbsd80-amd64"
STAGE=$(mktemp -d)
ROOT="$STAGE/$NAME"

echo "== puffy $VERSION"
go build -mod=vendor -trimpath -tags sqlite_fts5 -ldflags "-s -w" -o puffy .

echo "== sqlite-vector"
(cd "$VECTOR_SRC" && gmake CC="cc -fPIC" CPUS=2 extension >/dev/null)

echo "== llama-server (CPU only, baseline amd64: no AVX needed)"
# Same commit as the embedder that built the knowledge base, so query
# vectors match document vectors. Static ggml: no shared libs to ship.
(cd "$LLAMA_SRC" && cmake -B build-cpu -DCMAKE_BUILD_TYPE=Release \
    -DBUILD_SHARED_LIBS=OFF -DGGML_NATIVE=OFF -DGGML_AVX=OFF \
    -DGGML_AVX2=OFF -DGGML_AVX512=OFF -DGGML_FMA=OFF -DGGML_F16C=OFF \
    -DGGML_BMI2=OFF -DGGML_OPENMP=OFF -DLLAMA_CURL=OFF \
    -DLLAMA_OPENSSL=OFF -DLLAMA_BUILD_TESTS=OFF \
    -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_SERVER=ON >/dev/null &&
    cmake --build build-cpu --target llama-server -j 4 >/dev/null)

echo "== stage"
install -d "$ROOT/bin" "$ROOT/libexec/puffy" "$ROOT/lib/puffy" \
    "$ROOT/share/puffy" "$ROOT/share/doc/puffy"
# install.sh checks this list; get.sh checks the signed tarball.
install -m 755 puffy "$ROOT/bin/puffy"
install -m 755 "$LLAMA_SRC/build-cpu/bin/llama-server" "$ROOT/libexec/puffy/"
strip "$ROOT/libexec/puffy/llama-server"
install -m 644 "$VECTOR_SRC/dist/vector.so" "$ROOT/lib/puffy/vector.so"
install -m 644 "$DATA/puffy-kb.db" \
    "$DATA/nomic-embed-text-v1.5.Q8_0.gguf" "$ROOT/share/puffy/"
install -m 644 README.md LICENSE NOTICE docs/SETUP.md puffy-release.pub \
    "$ROOT/share/doc/puffy/"
install -m 755 dist/install.sh "$ROOT/install.sh"
install -m 755 dist/install.sh "$ROOT/share/puffy/install.sh"

echo "== runtime libraries (base system only)"
ldd "$ROOT/bin/puffy" "$ROOT/libexec/puffy/llama-server" |
    awk '$NF ~ /\.so/ {print "   ", $NF}' | sort -u

(cd "$ROOT" && find . -type f ! -name SHA256 -exec sha256 {} + > SHA256)
tar -C "$STAGE" -czf puffy-openbsd80-amd64.tgz "$NAME"
sha256 puffy-openbsd80-amd64.tgz > SHA256
rm -rf "$STAGE"
ls -l puffy-openbsd80-amd64.tgz
cat SHA256

#!/bin/sh
# provision.sh — copy puffy into the running test VM, build it there, and
# install it. Run vm/vm.sh start first.
#
#   vm/provision.sh            packages, sources, data, build, install
#   vm/provision.sh build      only resend the Go sources and rebuild

set -eu

# macOS tar adds AppleDouble ._* files for extended attributes; build
# systems that glob *.c or *.cpp would try to compile them.
export COPYFILE_DISABLE=1

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT="$HERE/.."
SSH="$HERE/vm.sh ssh"

send_sources() {
    tar -C "$ROOT" -czf - --exclude ./build --exclude ./data --exclude ./vm \
        --exclude ./ssa --exclude ./trusted_router_key --exclude ./puffy \
        --exclude ./vector.so . |
        $SSH 'rm -rf /home/build/puffy-src && mkdir -p /home/build/puffy-src &&
            tar -C /home/build/puffy-src -xzf -'
}

build_and_install() {
    $SSH 'cd /home/build/puffy-src && cp /home/build/vector.so . && make &&
        mkdir -p data && ln -sf /home/build/data/* data/ && make install &&
        ls -la /usr/local/bin/puffy'
}

if [ "${1:-}" = build ]; then
    send_sources
    build_and_install
    exit
fi

echo "== packages"
$SSH 'mkdir -p /home/build && pkg_add -I go sqlite3 llama.cpp gmake && go version'

echo "== sqlite-vector"
tar -C "$ROOT/build" -czf - --exclude ./sqlite-vector/build \
    --exclude ./sqlite-vector/dist --exclude ./sqlite-vector/.git \
    ./sqlite-vector | $SSH 'rm -rf /home/build/sqlite-vector &&
        tar -C /home/build -xzf - && cd /home/build/sqlite-vector &&
        gmake CC="cc -fPIC" CPUS=2 extension && cp dist/vector.so /home/build/'

echo "== data"
$SSH 'mkdir -p /home/build/data'
for file in "$ROOT/data/puffy-kb.db" \
    "$ROOT/build/dl/nomic-embed-text-v1.5.Q8_0.gguf"; do
    scp -q -i "$HERE/id_puffy_vm" -P "${PUFFY_VM_SSH_PORT:-2222}" \
        -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR "$file" root@127.0.0.1:/home/build/data/
done

echo "== build"
send_sources
build_and_install

echo "== user setup"
# The test user gets doas without a password so automated runs can do
# root work. On a real machine use "permit persist" instead.
$SSH 'echo "permit nopass puffy" > /etc/doas.conf &&
    install -d -o puffy -g puffy -m 700 /home/puffy/.puffy'
tr -d '\n' < "$ROOT/trusted_router_key" | $SSH \
    'cat > /home/puffy/.puffy/key && chown puffy /home/puffy/.puffy/key &&
        chmod 600 /home/puffy/.puffy/key'
echo "provisioned"

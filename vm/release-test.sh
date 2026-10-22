#!/bin/sh
# release-test.sh — install the published release on a clean VM with the
# exact one-line command from the README, then check it works: no
# packages, offline search, doctor, and one online task.
#
#   PUFFY_VM_DISK=vm/clean80.qcow2 PUFFY_VM_SSH_PORT=2223 vm/release-test.sh

set -u

HERE=$(cd "$(dirname "$0")" && pwd)
VM="$HERE/vm.sh"
FAILED=0

step() { printf '\n== %s\n' "$1"; }
pass() { echo "PASS $1"; }
fail() { echo "FAIL $1"; FAILED=$((FAILED + 1)); }

step "packages before install (expect none)"
count=$("$VM" ssh 'pkg_info -q | wc -l' | tr -d ' ')
echo "$count packages"

step "the one-line install, as root (doas is not set up on a fresh box)"
if "$VM" ssh 'ftp -o - https://raw.githubusercontent.com/elevenarms/puffy/main/get.sh | sh'; then
	pass install
else
	fail install
fi

step "still no packages"
after=$("$VM" ssh 'pkg_info -q | wc -l' | tr -d ' ')
[ "$after" = "$count" ] && pass "no packages added ($after)" || fail "packages changed"

step "offline search as user puffy (no key, no network needed)"
"$VM" ssh "su -l puffy -c 'puffy --search \"configure a static ip address\"'" |
	tee /dev/stderr | head -1 | grep -q 'hostname.if(5)' &&
	pass "search" || fail "search"

step "offline mode for a task (no key yet)"
"$VM" ssh "su -l puffy -c 'puffy how do I rotate log files </dev/null'" |
	tee /dev/stderr | grep -q 'newsyslog' &&
	pass "offline answer" || fail "offline answer"

step "add the API key and run doctor"
tr -d '\n' < "$HERE/../trusted_router_key" | "$VM" ssh \
	'install -d -o puffy -g puffy -m 700 /home/puffy/.puffy &&
	cat > /home/puffy/.puffy/key && chown puffy /home/puffy/.puffy/key &&
	chmod 600 /home/puffy/.puffy/key'
"$VM" ssh "su -l puffy -c 'puffy --doctor'" | tee /dev/stderr |
	grep -q FAIL && fail doctor || pass doctor

step "online task as puffy (uptime; no root needed)"
"$VM" ssh "su -l puffy -c 'PUFFY_NO_ASK=1 puffy show how long this machine has been up and its load </dev/null'" |
	tee /dev/stderr | grep -q 'load averages' &&
	pass "online task" || fail "online task"

echo
[ "$FAILED" -eq 0 ] && echo "release test passed" || echo "release test: $FAILED failed"
[ "$FAILED" -eq 0 ]

#!/bin/sh
# test.sh — run puffy inside the test VM as user "puffy" on real tasks and
# check the system afterwards. Run vm/vm.sh start and vm/provision.sh first.
#
#   vm/test.sh            all scenarios
#   vm/test.sh NAME ...   only these (doctor search learn ntpd code pkg tty)

set -u

HERE=$(cd "$(dirname "$0")" && pwd)
RESULTS="$HERE/results"
mkdir -p "$RESULTS"
PASSED=0
FAILED=0

as_puffy() {
    # No terminal on this ssh, so PUFFY_NO_ASK=1 lets changes run.
    "$HERE/vm.sh" ssh "su -l puffy -c 'cd /home/puffy &&
        PUFFY_NO_ASK=1 $1'"
}

verdict() {
    if [ "$2" -eq 0 ]; then
        echo "PASS $1"; PASSED=$((PASSED + 1))
    else
        echo "FAIL $1 (see vm/results/$1.txt)"; FAILED=$((FAILED + 1))
    fi
}

# check NAME REMOTE-COMMAND: pass when the command succeeds in the VM.
check() {
    name=$1; shift
    "$HERE/vm.sh" ssh "$@" >> "$RESULTS/$name.txt" 2>&1
    verdict "$name" $?
}

LATEST_LOG='$(ls -t /home/puffy/.puffy/logs/*.puffylog | head -1)'

scenario_doctor() {
    as_puffy 'puffy --doctor' > "$RESULTS/doctor.txt" 2>&1
    ! grep -q FAIL "$RESULTS/doctor.txt" && grep -q embedder \
        "$RESULTS/doctor.txt"
    verdict doctor $?
}

scenario_search() {
    as_puffy 'puffy --search "configure a static ip address"' \
        > "$RESULTS/search.txt" 2>&1
    head -3 "$RESULTS/search.txt" | grep -q 'hostname.if(5)'
    verdict search $?
}

scenario_learn() {
    as_puffy 'puffy "Teach me how OpenBSD decides which daemons start at
        boot: which files are involved and which command I should use.
        Look it up in the man pages and do not change anything." < /dev/null' \
        > "$RESULTS/learn.txt" 2>&1
    check learn "grep -q '^=== action \\(search\\|man\\)' $LATEST_LOG &&
        grep -A30 '^=== action complete' $LATEST_LOG >/dev/null &&
        tail -40 $LATEST_LOG | grep -q 'rc.conf.local'"
}

scenario_ntpd() {
    "$HERE/vm.sh" ssh 'rcctl stop ntpd; rcctl disable ntpd' >/dev/null 2>&1
    as_puffy 'puffy "ntpd is disabled on this machine. Enable it so it
        starts at boot, start it now, and verify that it is running." \
        < /dev/null' > "$RESULTS/ntpd.txt" 2>&1
    check ntpd 'rcctl ls on | grep -qx ntpd && rcctl check ntpd'
}

scenario_code() {
    "$HERE/vm.sh" ssh 'rm -rf /home/puffy/hello' >/dev/null 2>&1
    as_puffy 'puffy "In /home/puffy/hello write a small C program hello.c
        that prints the hostname using gethostname(3), restricts itself
        with pledge(2) to stdio before printing, and has a Makefile using
        the base system make. Build it with make and run it." < /dev/null' \
        > "$RESULTS/code.txt" 2>&1
    check code 'cd /home/puffy/hello && grep -q pledge hello.c &&
        ./hello | grep -q puffy'
}

scenario_pkg() {
    "$HERE/vm.sh" ssh 'pkg_delete -I tree' >/dev/null 2>&1
    as_puffy 'puffy "Install the tree package and show the directory tree
        of /etc/examples, two levels deep." < /dev/null' \
        > "$RESULTS/pkg.txt" 2>&1
    check pkg 'pkg_info -e "tree-*"'
}

# Interactive: approve with the real tty prompt. Answer "e" (explain) to
# the first action, then "y" to everything after.
scenario_tty() {
    expect -c "
        set timeout 600
        spawn $HERE/vm.sh ssh -t {su -l puffy -c 'puffy show the
            uptime and load of this machine'}
        set explained 0
        expect {
            -re {\? \[y\]es \[n\]o \[e\]xplain \[q\]uit: } {
                if {\$explained} { send \"y\r\" } else {
                    send \"e\r\"; set explained 1 }
                exp_continue
            }
            -re {puffy> \(follow-up task} { send \"\r\" }
            timeout { exit 1 }
        }
        expect eof
    " > "$RESULTS/tty.txt" 2>&1
    check tty "grep -q 'user asked for an explanation' $LATEST_LOG &&
        grep -q '^=== script output (exit 0)' $LATEST_LOG"
}

SCENARIOS=${*:-doctor search learn ntpd code pkg tty}
for name in $SCENARIOS; do
    : > "$RESULTS/$name.txt"
    echo "== $name"
    "scenario_$name"
done
echo "passed $PASSED, failed $FAILED"
[ "$FAILED" -eq 0 ]

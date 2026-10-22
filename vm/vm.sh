#!/bin/sh
# vm.sh — build, install and run the OpenBSD 8.0 amd64 test VM in QEMU.
#
#   vm/vm.sh iso       build puffy-install.iso (serial console, sets on cd0)
#   vm/vm.sh install   autoinstall OpenBSD onto vm/openbsd80.qcow2
#   vm/vm.sh start     boot the installed VM in the background
#   vm/vm.sh console   boot it in a Terminal window on the serial console
#   vm/vm.sh ssh [...] ssh into the VM as root (port 2222)
#   vm/vm.sh stop      halt -p the VM
#
# On Apple Silicon this is x86 emulation (TCG); expect it to be slow.

set -eu

HERE=$(cd "$(dirname "$0")" && pwd)
DOWNLOADS="$HERE/../build/dl"
ISO="$HERE/puffy-install.iso"
DISK="${PUFFY_VM_DISK:-$HERE/openbsd80.qcow2}"
SSH_PORT="${PUFFY_VM_SSH_PORT:-2222}"
HTTP_PORT="${PUFFY_VM_HTTP_PORT:-8000}"
SETS="bsd bsd.mp bsd.rd base80.tgz comp80.tgz man80.tgz xbase80.tgz BUILDINFO INSTALL.amd64"

qemu_args() {
    printf '%s ' -M q35 -accel tcg,thread=multi -cpu Nehalem -smp 4 -m 4096 \
        -drive "file=$DISK,if=virtio,format=qcow2" \
        -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:$SSH_PORT-:22" \
        -device virtio-net-pci,netdev=n0 -nographic
}

make_iso() {
    tree=$(mktemp -d)
    mkdir -p "$tree/8.0/amd64" "$tree/etc"
    for file in $SETS cdbr cdboot; do
        cp "$DOWNLOADS/$file" "$tree/8.0/amd64/"
    done
    # cdboot reads /etc/boot.conf: put the console on the serial port so
    # the installer can be driven from a terminal (and from expect).
    printf 'stty com0 115200\nset tty com0\n' > "$tree/etc/boot.conf"
    xorriso -as mkisofs -quiet -R -iso-level 3 -l -d -N -allow-lowercase \
        -relaxed-filenames -V OPENBSD80 \
        -b 8.0/amd64/cdbr -c 8.0/amd64/boot.catalog \
        -no-emul-boot -boot-load-size 4 -o "$ISO" "$tree"
    rm -rf "$tree"
    echo "$ISO"
}

install_vm() {
    [ -f "$ISO" ] || make_iso
    sed "s|@PUBKEY@|$(cat "$HERE/id_puffy_vm.pub")|" \
        "$HERE/install.conf.in" > "$HERE/install.conf"
    rm -f "$DISK"
    qemu-img create -q -f qcow2 "$DISK" 24G
    (cd "$HERE" && exec python3 -m http.server -b 127.0.0.1 "$HTTP_PORT") \
        >/dev/null 2>&1 &
    server=$!
    trap 'kill $server 2>/dev/null' EXIT
    # PXE: QEMU's built-in DHCP/TFTP hands out pxeboot, which reads
    # tftp/etc/boot.conf (serial console) and loads bsd.rd. A netbooted
    # installer starts autoinstall(8) on its own; sets come from cd0.
    mkdir -p "$HERE/tftp/etc"
    cp "$DOWNLOADS/pxeboot" "$DOWNLOADS/bsd.rd" "$HERE/tftp/"
    printf 'stty com0 115200\nset tty com0\nboot tftp:/bsd.rd\n' \
        > "$HERE/tftp/etc/boot.conf"
    # shellcheck disable=SC2046
    expect "$HERE/autoinstall.exp" "$HTTP_PORT" \
        qemu-system-x86_64 $(qemu_args | sed "s|user,id=n0,|&tftp=$HERE/tftp,bootfile=pxeboot,|") \
        -cdrom "$ISO" -boot once=n
}

start_vm() {
    # shellcheck disable=SC2046
    qemu-system-x86_64 $(qemu_args | sed 's/-nographic//') \
        -display none -serial "file:$HERE/console.log" \
        -daemonize -pidfile "$DISK.pid"
    echo "waiting for sshd on port $SSH_PORT"
    tries=0
    until ssh_vm true 2>/dev/null; do
        tries=$((tries + 1))
        [ "$tries" -lt 120 ] || { echo "VM did not come up" >&2; exit 1; }
        sleep 5
    done
    echo "VM is up"
}

# A QEMU window on the VGA console (ttyC0) for trying puffy by hand. The
# serial console still goes to console.log; ssh works as with start.
window_vm() {
    # shellcheck disable=SC2046
    nohup qemu-system-x86_64 $(qemu_args | sed 's/-nographic//') \
        -display cocoa,zoom-to-fit=on -name "puffy - OpenBSD 8.0" \
        -serial "file:$HERE/console.log" -pidfile "$DISK.pid" \
        > "$HERE/window.log" 2>&1 &
    echo "QEMU window opening; log in as puffy (password puffy)"
}

# The serial console (com0, where OpenBSD's getty runs) in a macOS
# Terminal window: scrollback, copy/paste and colors. Ctrl-A X quits QEMU.
console_vm() {
    script="$HERE/.console.command"
    # shellcheck disable=SC2046
    printf '#!/bin/sh\nprintf "\\033]0;puffy - OpenBSD 8.0\\007"\n%s\n' \
        "exec qemu-system-x86_64 $(qemu_args) -serial mon:stdio" > "$script"
    chmod +x "$script"
    open -a Terminal "$script"
    echo "Terminal window opening; log in as puffy (password puffy)"
    echo "Ctrl-A X stops the VM (halt -p as root first is cleaner)"
}

ssh_vm() {
    ssh -i "$HERE/id_puffy_vm" -p "$SSH_PORT" -o BatchMode=yes \
        -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR -o ConnectTimeout=10 root@127.0.0.1 "$@"
}

stop_vm() {
    ssh_vm halt -p || true
    sleep 10
    [ -f "$DISK.pid" ] && kill "$(cat "$DISK.pid")" 2>/dev/null
    rm -f "$DISK.pid"
}

case "${1:-}" in
    iso) make_iso ;;
    install) install_vm ;;
    start) start_vm ;;
    window) window_vm ;;
    console) console_vm ;;
    ssh) shift; ssh_vm "$@" ;;
    stop) stop_vm ;;
    *) sed -n '2,12p' "$0"; exit 1 ;;
esac

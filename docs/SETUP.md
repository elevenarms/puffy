# Setting up puffy

This guide takes you from a fresh OpenBSD 8.0 amd64 machine to a working
puffy. It takes about ten minutes, most of it the download.

puffy works in two modes:

- **Online:** a TrustedRouter model plans and runs tasks with you. The
  default route, `trustedrouter/e2e`, uses only providers with attested
  confidential compute, end-to-end encryption and zero data retention.
- **Offline:** with no network or no API key, puffy still searches and
  reads the OpenBSD 8.0 manual pages and package catalog on your disk.
  It switches on its own, and `retry` goes back online when you can.

## What you need

- OpenBSD 8.0 on amd64. Any 64-bit x86 CPU works: the bundled embedder
  is built without AVX, for CPUs that only run inference on the CPU.
- About 600 MB of disk in `/usr/local` and 300 MB of free memory while
  searching.
- A normal user account with `doas` set up, for the tasks that need root.
- For online mode: a TrustedRouter API key from https://trustedrouter.com.

No packages are needed. puffy uses only base system libraries.

## 1. Set up doas (if you have not)

puffy runs as your normal user. When a step needs root, its script uses
doas(1), which asks for your password. As root:

```sh
echo 'permit persist :wheel' > /etc/doas.conf
```

That lets members of the wheel group use doas, asking for a password
once every few minutes. See doas.conf(5) for other rules.

## 2. Install (one command)

As your normal user (with doas), or as root without `doas`:

```sh
ftp -o - https://raw.githubusercontent.com/elevenarms/puffy/main/get.sh | doas sh
```

[`get.sh`](../get.sh) downloads the latest release with ftp(1) and
verifies it with signify(1) against the puffy release key, the same way
OpenBSD verifies its own sets. Then it runs the bundled `install.sh`. If
the signature does not match, nothing is installed. To pin a version, use
`... | doas env PUFFY_RELEASE=v0.1.0 sh`.

Prefer to read the script first? Download it, read it, then run it:

```sh
ftp https://raw.githubusercontent.com/elevenarms/puffy/main/get.sh
less get.sh && doas sh get.sh
```

### Or by hand

```sh
ftp https://github.com/elevenarms/puffy/releases/latest/download/puffy-openbsd80-amd64.tgz
ftp https://github.com/elevenarms/puffy/releases/latest/download/SHA256.sig
ftp https://raw.githubusercontent.com/elevenarms/puffy/main/puffy-release.pub
signify -C -p puffy-release.pub -x SHA256.sig puffy-openbsd80-amd64.tgz
tar xzf puffy-openbsd80-amd64.tgz
doas ./puffy-*-openbsd80-amd64/install.sh
```

The release key fingerprint is the second line of
[`puffy-release.pub`](../puffy-release.pub):
`RWRT3j9AugHfOIahtX9ZAjhOmAuwCTBcBwK8G6O0MQeO4HwED0EudSi8`

`install.sh` checks every file against the bundled `SHA256` list and
copies:

| File | What it is |
| --- | --- |
| `/usr/local/bin/puffy` | the program |
| `/usr/local/libexec/puffy/llama-server` | local embedder, CPU only |
| `/usr/local/lib/puffy/vector.so` | sqlite-vector extension |
| `/usr/local/share/puffy/puffy-kb.db` | man pages and packages, indexed (255 MB) |
| `/usr/local/share/puffy/nomic-embed-text-v1.5.Q8_0.gguf` | embedding model (146 MB) |
| `/usr/local/share/puffy/install.sh` | this installer, for `-u` (remove) |
| `/usr/local/share/doc/puffy/` | README, this guide, licenses, release key |

To remove it: `doas sh /usr/local/share/puffy/install.sh -u`. Your
`~/.puffy` stays.

## 3. First run

As your normal user, run `puffy`. The first run walks you through setup:

1. A short explanation of how puffy works.
2. Your TrustedRouter key, typed hidden, checked against the router, and
   saved to `~/.puffy/key` with mode 0600. Press Enter with no key to
   use offline mode only. If the network is down, the key is saved
   unchecked.
3. A model. Only routes in TrustedRouter's confidential tier are offered.
4. Teaching on or off. With it on, puffy explains each step and names the
   man pages behind it.
5. A check of the knowledge base, the embedder and doas, with the exact
   fix for anything missing.

Settings go to `~/.puffy/config`. Run `puffy --setup` to change them and
`puffy --doctor` to check everything later.

## 4. Use it

```sh
puffy --search "redirect a port with pf"   # local docs, no model, no network
puffy explain how OpenBSD boots, step by step
puffy enable ntpd and show me how to check it
puffy                                      # asks for a task, then follow-ups
```

Before puffy changes anything, it asks:

```
Run this script? [y]es [n]o [e]xplain [q]uit:
```

- **y** runs it.
- **n** refuses; you can tell the model why.
- **e** has the model explain every command, flag and file first, with
  man pages, then propose it again. Use it whenever you want to learn.
- **q** ends the run.

Every run is logged in full to `~/.puffy/logs/`.

## Offline use

puffy goes offline by itself when the router cannot be reached (no
network, DNS failure, timeout) or when there is no key. To force it:
`PUFFY_OFFLINE=1 puffy`. Offline, puffy shows what the local docs say
about your task and gives you a prompt:

```
puffy offline> wireguard              search
puffy offline> man wg(4) EXAMPLES     read a page or one section
puffy offline> retry                  try the model again
```

When the network is back, `retry` resumes the same task with the
conversation intact.

## Colors

puffy colors its output only on a terminal. `NO_COLOR=1` or `TERM=dumb`
turns color off, and `PUFFY_COLOR=always|never` overrides both. Text from
the model has control characters removed before it reaches your terminal.

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| `!! model offline` in `--doctor` | network or DNS; search still works |
| `401 Unauthorized` | wrong key: `puffy --setup` |
| `402 ... Insufficient credits` | add credit; puffy already halves `max_tokens` |
| search finds only keywords | `--doctor` names the missing embedder or vector.so |
| `doas: not permitted` | step 1 |
| first search slow | the embedder loads once, 2 to 5 seconds |

## Building from source

See README.md: building puffy, `vector.so` and `llama-server` on OpenBSD,
rebuilding the knowledge base on a Mac, and the QEMU test VM.

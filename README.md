```
          .   \   |   /   .
        '. \ .-'"""""'-. / .'
     -._  .'             '.  _.-
        '/          .-.    \'
   |\   |    ,     ( o )    |
   | >==|   (       '-'     o)
   |/   |    `              |
       .\                   /.
     -'  '.               .'  '-
        .' '-._________.-' '.
          '   /   |   \   '
```

# puffy

*An OpenBSD native AI agent that reads the man pages so it can tell you
which man page to read.*

Project page: https://elevenarms.github.io/puffy/

puffy is an OpenBSD system management, configuration, maintenance and
programming agent that teaches as it works. It runs on your OpenBSD
machine, plans with a TrustedRouter model on confidential compute, and
looks things up in the OpenBSD 8.0 manual pages and package catalog,
stored and searched locally. With no network it falls back to that local
search on its own.

```
$ puffy enable ntpd and show me how to check it

# rcctl(8) manages daemons: "enable" writes ntpd_flags to
# /etc/rc.conf.local so ntpd starts at boot; "start" starts it now.
# script
doas rcctl enable ntpd
doas rcctl start ntpd
doas rcctl check ntpd
# end
Run this script? [y]es [n]o [e]xplain [q]uit:
```

Install on OpenBSD 8.0 amd64 (no packages needed; signify-verified):

```sh
ftp -o - https://raw.githubusercontent.com/elevenarms/puffy/main/get.sh | doas sh
```

Then run `puffy` as your normal user. The first run walks you through
setup. Details, manual install and troubleshooting:
[docs/SETUP.md](docs/SETUP.md).

## What it does

- **Does the work with you.** Each reply from the model is one action: a
  shell script, a file write, an edit, a documentation lookup, a question
  or a message. puffy asks before anything that changes the system.
  Answer **e** to have the model explain every command and flag first.
- **Teaches OpenBSD.** It prefers base system tools (rcctl, pf, httpd,
  doas, syspatch), validates configs before loading them, names the man
  pages behind each step, and ends with what changed, how to undo it, and
  what to read next.
- **Knows OpenBSD 8.0.** All 5,400+ amd64 manual pages and 12,571
  packages (with their pkg-readmes) are indexed on disk. The model reads
  them through `# search` and `# man:` actions, and so can you:
  `puffy --search "redirect a port with pf"`.
- **Private by default.** Inference goes to `trustedrouter/e2e`: attested
  confidential compute, end-to-end encryption, zero data retention.
  Document search never leaves the machine.
- **Works offline.** No network or no key: puffy answers from the local
  docs and keeps a search prompt open, and `retry` resumes the task.

## How it works

The loop follows [ssa](https://github.com/patrickjh/ssa): the model
answers in plain text with exactly one action, ending in a line `# end`,
and the result comes back as the next turn. There are no JSON tool calls,
so any chat model works, including confidential ones that offer no tool
support. puffy stops reading the stream at `# end`, so the model cannot
make up the output of a command it has not run.

| Reply | puffy | Approval |
| --- | --- | --- |
| `# script` | runs the body with `/bin/sh` | asked |
| `# write file: PATH` | writes the body to PATH | asked |
| `# edit file: PATH` | SEARCH/REPLACE blocks, each must match once | asked |
| `# search` / `# search man` / `# search pkg` | searches the local docs | none |
| `# man: NAME(SECTION)` | reads a page, or the sections matching a line | none |
| `# question` | asks you and returns your answer | — |
| `# message` | shows text; the run continues | — |
| `# complete` | shows the summary; the task ends | — |

**Search** fuses three ranked lists with reciprocal rank fusion:
- BM25 over section text, using SQLite FTS5.
- Vector similarity over sections.
- Vector similarity over "page cards", the one-line NAME of each page.

A small concept map (`concepts.go`) ties everyday words to OpenBSD names,
such as "sudo" to doas(1) and "service" to rcctl(8).

**Vectors** come from nomic-embed-text-v1.5, run locally by a CPU-only
llama-server. They are stored as float16 and scanned through a
[sqlite-vector](https://github.com/sqliteai/sqlite-vector) TurboQuant
4-bit index. puffy takes 80 candidates from that index and re-ranks them
exactly. `kb/eval_quant.py` measured 99.9% of the exact top 40, with an
identical top 10, at one sixth of the size.

`pledge(2)`: once setup is done, puffy pledges `stdio rpath wpath cpath
dpath fattr chown flock tty proc exec inet dns`. Approved scripts run
unpledged, because they must be able to do whatever you could.

## Settings

Settings are `PUFFY_*` environment variables. Onboarding writes the main
ones to `~/.puffy/config`, and the environment overrides that file.

| Setting | Default |
| --- | --- |
| `PUFFY_MODEL` | `trustedrouter/e2e` |
| `PUFFY_API_BASE` | `https://api.trustedrouter.com/v1` |
| `PUFFY_API_KEY` / `PUFFY_API_KEY_FILE` | unset / `~/.puffy/key` |
| `PUFFY_OFFLINE` | `0`; `1` never calls the model |
| `PUFFY_MAX_TOKENS` | `4096` (halved on HTTP 402) |
| `PUFFY_KB` | `/usr/local/share/puffy/puffy-kb.db` |
| `PUFFY_VECTOR_EXT` | `/usr/local/lib/puffy/vector.so` |
| `PUFFY_EMBED_MODEL` | `/usr/local/share/puffy/nomic-embed-text-v1.5.Q8_0.gguf` |
| `PUFFY_EMBED_SERVER` | `/usr/local/libexec/puffy/llama-server`, else `llama-server` on PATH |
| `PUFFY_EMBED_URL` | unset; puffy starts the embedder on 127.0.0.1 when needed |
| `PUFFY_LOG_FOLDER` | `~/.puffy/logs` |
| `PUFFY_SCRIPT_TIMEOUT` | `900` seconds |
| `PUFFY_OUTPUT_CHARS` | `16000` (head and tail of longer output) |
| `PUFFY_CONTEXT_CHARS` | `300000` (oldest turns drop first) |
| `PUFFY_NO_ASK` | `0`; with no terminal, changes are refused unless `1` |
| `PUFFY_NO_SCRIPTS`, `PUFFY_NO_WRITES` | `0` |
| `PUFFY_NO_TEACH` | `0` |
| `PUFFY_QUIET` | `0` |
| `PUFFY_COLOR` | `auto` (terminal only; off with `NO_COLOR` or `TERM=dumb`), `always`, `never` |
| `PUFFY_EXTRA_SYSTEM_TEXT` | empty, appended to the system prompt |

Exit status: 0 done, 1 setup error, failed model request or quit, 130
interrupted.

## Building from source

On OpenBSD 8.0 amd64, with `doas pkg_add go gmake cmake`:

```sh
git clone https://github.com/elevenarms/puffy && cd puffy
git clone https://github.com/sqliteai/sqlite-vector ../sqlite-vector
git clone https://github.com/ggml-org/llama.cpp ../llama.cpp
(cd ../llama.cpp && git checkout 620d2225c)    # same as the KB build
# data/: puffy-kb.db and the .gguf, from a release or built below
dist/make-dist.sh ../sqlite-vector ../llama.cpp data
```

That builds `puffy` (Go, vendored modules, `-tags sqlite_fts5`),
`vector.so` and a CPU-only `llama-server`, then packs the release
tarball. For quick development builds use `make`.

### The knowledge base (one time, on a Mac)

```sh
brew install llama.cpp
# into build/dl: man80 comp80 game80 xshare80 sets, packages/amd64/
# index.txt, sqlports-*.tgz, nomic-embed-text-v1.5.Q8_0.gguf
(cd build/sqlite-vector && make ARCH=arm64 CC=clang)
llama-server -m build/dl/nomic-embed-text-v1.5.Q8_0.gguf --embedding \
    --pooling mean -c 16384 -np 4 -b 4096 -ub 4096 --port 8089 &
python3 kb/build_kb.py --man build/man \
    --sqlports build/sqlports/share/sqlports \
    --index build/dl/pkg-index.txt \
    --vector build/sqlite-vector/dist/vector --out data/puffy-kb.db
python3 kb/eval_quant.py --db data/puffy-kb.db \
    --vector build/sqlite-vector/dist/vector --work /tmp/eval.db
```

The build renders each page with `mandoc -T ascii` and splits it by
section (41k chunks). It adds the packages built for 8.0 from sqlports
(13k chunks) and embeds everything with a `search_document:` prefix
(about 15 minutes on an M-series Mac). It then embeds one card per page,
stores the vectors as float16 and builds the TurboQuant index. Queries
use `search_query:` with the same model, pooling and normalization.

### Test VM (QEMU on macOS)

```sh
vm/vm.sh install     # PXE + autoinstall(8), about 15 min under emulation
vm/vm.sh console     # boot in a Terminal window on the serial console
vm/vm.sh start       # or headless; ssh on 127.0.0.1:2222
vm/provision.sh      # build and install puffy inside
vm/test.sh           # real tasks as user puffy, checked from outside
```

## License

ISC; see [LICENSE](LICENSE). The release bundles sqlite-vector
(Apache-2.0), llama.cpp (MIT), nomic-embed-text-v1.5 (Apache-2.0) and
text derived from the OpenBSD manual pages and ports tree; see
[NOTICE](NOTICE).

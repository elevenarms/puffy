# AGENTS.md

How to work on puffy. Behavior is in the Go sources; README.md is the
user help. ssa/ is the upstream design puffy follows; leave it unchanged.

## Layout

```
main.go        entry, --search, --doctor, --setup
settings.go    PUFFY_* environment settings
setup.go       first-run onboarding and ~/.puffy/config
prompt.go      system prompt and reply formats
agent.go       loop, context trimming, reply parsing
actions.go     approval, scripts, writes, edits, search and man actions
llm.go         streaming /chat/completions client (stops at "# end")
kb.go          knowledge base: FTS5 + vector fused search
vectors.go     TurboQuant scan + float16 re-rank, half-float helpers
offline.go     offline mode: local search when the model is unreachable
term.go        terminal color and control-character cleaning
concepts.go    everyday words -> OpenBSD page names
embed.go       local llama-server for query embeddings
restrict_*.go  pledge(2) on OpenBSD
kb/build_kb.py one-time knowledge base build (Mac)
kb/eval_quant.py  recall of quantized scans vs exact
dist/          release tarball build and install.sh
docs/SETUP.md  user setup guide
vm/            QEMU install, start, provision
```

## Rules

- Keep ssa's protocol: one plain-text action per reply, ending in `# end`.
  Do not add JSON tool calls; most confidential models do not support them.
- The query embedder must be the same model, prefix (`search_query: `),
  pooling (mean) and normalization as the build. Change both or neither.
- Retrieval never uses the network. Only the chat request leaves the host.
- Network failure means offline mode, never a crash; HTTP errors from a
  reachable router (401, 402) are reported, not treated as offline.
- Changing the quantization? Run kb/eval_quant.py first and record the
  numbers in kb/build_kb.py.
- Read-only actions (search, man) need no approval; anything that
  changes the system does. With no terminal, changes are refused unless
  PUFFY_NO_ASK=1.
- Every user-facing setting is a `PUFFY_*` variable documented in
  README.md. Name the variable in error messages the user can fix.
- Full log of every run. Do not drop records a reviewer would need.
- Go: standard library first; dependencies are vendored (`go mod vendor`)
  because the VM build is offline. Build tag `sqlite_fts5` is required.

## Test

```sh
go test -tags sqlite_fts5 .
go build -tags sqlite_fts5 -o build/puffy . && build/puffy --doctor
vm/provision.sh build      # rebuild inside the VM after source changes
```

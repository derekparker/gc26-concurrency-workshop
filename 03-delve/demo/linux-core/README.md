# Pre-captured linux/amd64 core dump

A frozen `pipeline` deadlock, captured on Linux, for the Part III
post-mortem beat. `setup.sh` unpacks the two `.gz` files; if you'd rather do
it by hand:

```bash
gunzip -k pipeline.linux-amd64.gz pipeline.linux-amd64.core.gz
chmod +x pipeline.linux-amd64
dlv core ./pipeline.linux-amd64 ./pipeline.linux-amd64.core
```

Then, inside the session:

```
(dlv) config substitute-path delve-demo ..
(dlv) goroutines -with user
(dlv) goroutine 1
(dlv) frame 3
(dlv) print order.ID              # 18
(dlv) goroutines -chan p.shipping
```

## Why this is committed

Delve's `dump` on macOS writes Go's whole reserved virtual arena: this 6 MB
program produces a **~6.4 GB** core over about a minute. The identical dump
on Linux is **48 MB in 2.5 seconds** (Linux writes only mapped pages).
Rather than make that the live demo, we ship a Linux core.

Core dumps are read post-mortem, with no live process, so **neither the OS
nor the CPU architecture has to match the machine reading them**. This
linux/amd64 core opens fine on darwin/arm64, which is the lesson: a core
from a production Linux host is something you can open on your laptop.

## Provenance

| | |
|---|---|
| Captured | 2026-07-31 |
| Platform | linux/amd64 (Fedora 44, kernel 7.1.3) |
| Go | go1.26.5 (distro build, `X:nodwarf5`, i.e. DWARF 4) |
| Delve | 1.27.0 |
| Build | `go build -trimpath -gcflags='all=-N -l'` |
| Capture | Delve's own `dump` at the `runtime-fatal-throw` stop |
| Sizes | binary 2.7 MB / core 48 MB (0.8 MB gzipped) |

`-trimpath` is what records the source as `delve-demo/pipeline.go` rather
than an absolute build directory, so `substitute-path` can remap it onto any
checkout. The distro Go emits DWARF 4 rather than DWARF 5; Delve reads both,
and every command above was verified against these exact artifacts.

Regenerate with [`make-core.sh`](make-core.sh) on a Linux box or in a
container (the header comment has a one-line `docker run`).

# Official Linux memory probe oracle

Source: cached official Node v24.22.1 source archive, bundled libuv 1.52.1.
Upstream URLs:
- https://nodejs.org/dist/v24.22.1/node-v24.22.1.tar.xz
- https://github.com/libuv/libuv/blob/v1.52.1/src/unix/linux.c

`UPSTREAM.sha256` records the four complete official files. `source.inc` is
verbatim linux.c from `uv__read_proc_meminfo` through `uv_get_available_memory`.
It is covered by the adjacent upstream LICENSE. `prepare.py` verifies both the
complete-file hashes and the exact extracted segment before preparing inputs.
`oracle.c` replaces only IO/sysinfo/sysconf with controlled fixture reads; the
memory algorithm is compiled unchanged from `source.inc`. Neither downloaded
packages nor a full Node/libuv rebuild are needed.

Node `src/node_process_methods.cc:257-260` delegates process.availableMemory()
to uv_get_available_memory and converts the uint64 result to JavaScript Number.
Linux source `linux.c:2036-2088` uses MemAvailable/MemTotal first, then sysinfo.
`linux.c:2108-2256` implements the exact v1/v2 lookup/fallback rules.
`linux.c:2259-2287` returns zero for unreadable cgroup metadata, uses host free
only when unconstrained or constrained greater than host total, otherwise
returns constrained-current (saturating at zero). It does not traverse parents
or minimize constrained-current against host free. The r3 approximation did
both and is not suitable for publication.

The frozen runner prepares and compiles this small host C harness, writes its
results outside frozen inputs, then feeds those observations to native Go tests.
Tests compare both the numeric results and ordered IO calls. Fixtures are all
synthetic. This covers Linux only; the default non-Linux probe remains blocked
(return zero) until its platform-specific libuv equivalent is implemented.

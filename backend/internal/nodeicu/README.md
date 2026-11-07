# Shared pinned ICU initialization

`Ensure` is the only owner of process-wide `udata_setFileAccess`,
`udata_setCommonData` and `u_init`. Native `topiccollation`, `jsdate` and `intltime` call
it before using ICU. One `sync.Once` performs the existing 33,107,952-byte
SHA-256 verification and initialization. There is no per-consumer second hash,
host-file fallback, eager Node process, or ICU cleanup while consumers are live.

The `humanengine_icu && cgo` tag and pinned `backend/native/icu` build are
required. Untagged consumers receive `ErrUnavailable`; explicit UTC Date.parse
needs no ICU initialization. The native regression starts 128 simultaneous
first-use collation/date/hour consumers and observes exactly one initialization.
It also exercises actual collation and timezone operations after the barrier.

The native source-free target also runs the Date.parse and participation-hour corpora and this fresh-
process concurrent initialization test beside collation, with no Node executable,
source tree or ICU data files in the proof directory. Shared system C/C++ runtime
libraries remain dependencies; this is not a fully static production bot image.

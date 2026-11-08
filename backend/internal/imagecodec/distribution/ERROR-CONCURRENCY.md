# Concurrent error semantics

Verified upstream libvips v8.18.7 iofuncs/error.c: vips_error_text/error_buf are
process-global. vips_error_buffer() locks while returning a pointer, then unlocks;
the pointer can change while another native caller reads it. vips_error_buffer_copy()
locks, duplicates, clears and unlocks atomically. Upstream cplusplus/include/vips/
VError8.h constructs std::runtime_error(vips_error_buffer()); sharp pipeline/stats
workers clear errors and call vips_thread_shutdown at completion.

The bridge now captures failures inside the C wrapper with error_buffer_copy,
passes the owned copy to Go, frees it, and never reads mutable global text during
normal Compress calls. It calls vips_thread_shutdown at each native boundary.
Error text uses sharp TrimEnd's exact character set (space, newline, CR, FF).
No native pixel serialization lock or new protective concurrency cap is added.

This prevents the raw-pointer race; it cannot promise perfect attribution when
multiple libvips operations fail at the same time: shared diagnostics may be
combined or cleared by a different operation, as in the upstream sharp/libvips
path. Admission/decode failures are normalized to invalid_or_unsupported_image
by both source and bridge, making their outward mapping stable. Exact arbitrary
concurrent render-error strings are an explicit remaining upstream limitation.
Do not claim thread-local error buffers based on runtime.LockOSThread.

The concurrency default follows sharp's glibc/mallctl/MALLOC_ARENA_MAX branch.
Test harness VIPS_CONCURRENCY=1 is separately documented as resource coordination.

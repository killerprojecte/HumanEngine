/* Harness only. The memory algorithm is the unmodified upstream source.inc.
 * See LICENSE and UPSTREAM.sha256 for Node v24.22.1 / libuv 1.52.1 provenance. */
#include <assert.h>
#include <inttypes.h>
#include <limits.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <sys/sysinfo.h>
#include <unistd.h>

struct file_entry { const char* path; const char* contents; };
struct fixture { const char* name; const struct file_entry* files; size_t count; uint64_t free_bytes; uint64_t total_bytes; int sys_ok; uint64_t want; };
#include "fixtures.h"
static const struct fixture* active;
static int reads;
static void trace(const char* path, size_t size) {
  /* All probe paths here are fixed kernel paths or controlled ASCII fixtures. */
  printf("%s{\"Path\":\"%s\",\"Size\":%zu}", reads++ ? "," : "", path, size);
}
static int fixture_slurp(const char* path, char* out, size_t len) {
  size_t i, n;
  trace(path, len);
  for (i = 0; i < active->count; i++) {
    if (strcmp(path, active->files[i].path)) continue;
    n = strlen(active->files[i].contents);
    if (n >= len) n = len - 1;
    memcpy(out, active->files[i].contents, n);out[n] = '\0';return 0;
  }
  return -1;
}
static int fixture_sysinfo(struct sysinfo* info) {
  trace("sysinfo", 0);
  if (!active->sys_ok) return -1;
  memset(info, 0, sizeof(*info));info->mem_unit = 1;
  info->freeram = active->free_bytes;info->totalram = active->total_bytes;
  return 0;
}
static long fixture_sysconf(int name) { assert(name == _SC_PAGESIZE); return 4096; }
/* Only uv_get_constrained_memory (not the availableMemory path) uses this. */
static uint64_t uv__get_rlimit_max_memory(void) { return 0; }
#define uv__slurp fixture_slurp
#define sysinfo(info) fixture_sysinfo(info)
#define sysconf(name) fixture_sysconf(name)
#include "source.inc"

int main(void) {
  size_t i;
  int failed = 0;
  puts("[");
  for (i = 0; i < sizeof(fixtures) / sizeof(fixtures[0]); i++) {
    uint64_t available;
    active = &fixtures[i];reads = 0;
    printf("%s{\"Name\":\"%s\",\"Reads\":[", i ? "," : "", active->name);
    available = uv_get_available_memory();
    printf("],\"Available\":%" PRIu64 "}", available);
    if (available != active->want) {
      fprintf(stderr, "%s: upstream produced %" PRIu64 ", fixture expected %" PRIu64 "\n", active->name, available, active->want);
      failed = 1;
    }
  }
  puts("\n]");
  return failed;
}

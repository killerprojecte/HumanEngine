/* Linux test-only interposition. Never linked into a shipped adapter. */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <errno.h>
#include <netdb.h>
#include <stdint.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static _Atomic(uintptr_t) held_result;
static void boundary(char phase) {
 const char *events = getenv("HE_DNS_EVENTS");
 const char *gate = getenv("HE_DNS_GATE");
 if (!events || !gate) _exit(90);
 ssize_t n;
 do { n = write(atoi(events), &phase, 1); } while (n < 0 && errno == EINTR);
 if (n != 1) _exit(91);
 do { n = read(atoi(gate), &phase, 1); } while (n < 0 && errno == EINTR);
 if (n != 1) _exit(92);
}
int getaddrinfo(const char *name, const char *service, const struct addrinfo *hints, struct addrinfo **result) {
 int (*real_lookup)(const char *, const char *, const struct addrinfo *, struct addrinfo **) = dlsym(RTLD_NEXT, "getaddrinfo");
 if (!real_lookup) _exit(93);
 if (getenv("HE_DNS_LENGTH_ONLY")) {
  /* All fixture inputs are ASCII, including URL-normalized IDNA. */
  if (!name || strlen(name) >= 256) _exit(96);
  const char *events = getenv("HE_DNS_EVENTS");
  if (!events) _exit(97);
  if (write(atoi(events), "L", 1) != 1) _exit(98);
  *result = NULL;
  return EAI_FAIL;
 }
 if (!getenv("HE_DNS_EVENTS") || !name || strcmp(name, "127.0.0.1")) return real_lookup(name, service, hints, result);
 /* Verify exactly the production hints at the real FFI boundary. */
 if (!hints || hints->ai_family != AF_UNSPEC || hints->ai_socktype != SOCK_STREAM || hints->ai_flags != 0 || service != NULL) _exit(94);
 boundary('E');
 if (getenv("HE_DNS_FAIL")) { *result = NULL; return EAI_FAIL; }
 int status = real_lookup(name, service, hints, result);
 if (!status && *result) atomic_store(&held_result, (uintptr_t)*result);
 return status;
}
void freeaddrinfo(struct addrinfo *result) {
 void (*real_free)(struct addrinfo *) = dlsym(RTLD_NEXT, "freeaddrinfo");
 if (!real_free) _exit(95);
 uintptr_t expected = (uintptr_t)result;
 int held = result && atomic_compare_exchange_strong(&held_result, &expected, 0);
 real_free(result);
 /* Hold even after deallocation: Drained must wait for cleanup to return. */
 if (held) boundary('F');
}

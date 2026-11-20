/* Test-only synthetic libc. Do not forward a name to the real resolver. */
#include <arpa/inet.h>
#include <netdb.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static struct addrinfo rows[4];
static struct sockaddr_storage addresses[4];

int getaddrinfo(const char *name, const char *service, const struct addrinfo *hints,
                struct addrinfo **result) {
  const char *events = getenv("HE_RESOLVER_CHILD_EVENTS");
  if (!events || !name || service || !hints || hints->ai_family != AF_UNSPEC ||
      hints->ai_socktype != SOCK_STREAM || hints->ai_flags != 0) {
    _exit(92);
  }
  FILE *f = fopen(events, "a");
  if (!f) _exit(93);
  fprintf(f, "%d\n", getpid());
  fclose(f);

  if (!strcmp(name, "stall.fixture.invalid")) {
    for (;;) pause();
  }
  if (!strcmp(name, "denied.fixture.invalid")) {
    *result = NULL;
    return EAI_FAIL;
  }
  if (strcmp(name, "ordered.fixture.invalid")) _exit(94);

  const char *ordered[] = {"2606:4700:4700::1111", "8.8.8.8", "::ffff:8.8.8.8", "8.8.8.8"};
  for (int i = 0; i < 4; i++) {
    rows[i].ai_addr = (struct sockaddr *)&addresses[i];
    rows[i].ai_next = i < 3 ? &rows[i + 1] : NULL;
    if (strchr(ordered[i], ':')) {
      struct sockaddr_in6 *a = (struct sockaddr_in6 *)&addresses[i];
      rows[i].ai_family = a->sin6_family = AF_INET6;
      rows[i].ai_addrlen = sizeof(*a);
      if (inet_pton(AF_INET6, ordered[i], &a->sin6_addr) != 1) _exit(95);
    } else {
      struct sockaddr_in *a = (struct sockaddr_in *)&addresses[i];
      rows[i].ai_family = a->sin_family = AF_INET;
      rows[i].ai_addrlen = sizeof(*a);
      if (inet_pton(AF_INET, ordered[i], &a->sin_addr) != 1) _exit(96);
    }
  }
  *result = &rows[0];
  return 0;
}

void freeaddrinfo(struct addrinfo *result) {
  if (result != &rows[0]) _exit(97);
}

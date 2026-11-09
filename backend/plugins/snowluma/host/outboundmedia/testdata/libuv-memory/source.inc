static uint64_t uv__read_proc_meminfo(const char* what) {
  uint64_t rc;
  char* p;
  char buf[4096];  /* Large enough to hold all of /proc/meminfo. */

  if (uv__slurp("/proc/meminfo", buf, sizeof(buf)))
    return 0;

  p = strstr(buf, what);

  if (p == NULL)
    return 0;

  p += strlen(what);

  rc = 0;
  sscanf(p, "%" PRIu64 " kB", &rc);

  return rc * 1024;
}


uint64_t uv_get_free_memory(void) {
  struct sysinfo info;
  uint64_t rc;

  rc = uv__read_proc_meminfo("MemAvailable:");

  if (rc != 0)
    return rc;

  if (0 == sysinfo(&info))
    return (uint64_t) info.freeram * info.mem_unit;

  return 0;
}


uint64_t uv_get_total_memory(void) {
  struct sysinfo info;
  uint64_t rc;

  rc = uv__read_proc_meminfo("MemTotal:");

  if (rc != 0)
    return rc;

  if (0 == sysinfo(&info))
    return (uint64_t) info.totalram * info.mem_unit;

  return 0;
}


static uint64_t uv__read_uint64(const char* filename) {
  char buf[32];  /* Large enough to hold an encoded uint64_t. */
  uint64_t rc;

  rc = 0;
  if (0 == uv__slurp(filename, buf, sizeof(buf)))
    if (1 != sscanf(buf, "%" PRIu64, &rc))
      if (0 == strcmp(buf, "max\n"))
        rc = UINT64_MAX;

  return rc;
}


/* Given a buffer with the contents of a cgroup1 /proc/self/cgroups,
 * finds the location and length of the memory controller mount path.
 * This disregards the leading / for easy concatenation of paths.
 * Returns NULL if the memory controller wasn't found. */
static char* uv__cgroup1_find_memory_controller(char buf[static 1024],
                                                int* n) {
  char* p;

  /* Seek to the memory controller line. */
  p = strchr(buf, ':');
  while (p != NULL && strncmp(p, ":memory:", 8)) {
    p = strchr(p, '\n');
    if (p != NULL)
      p = strchr(p, ':');
  }

  if (p != NULL) {
    /* Determine the length of the mount path. */
    p = p + strlen(":memory:/");
    *n = (int) strcspn(p, "\n");
  }

  return p;
}

static void uv__get_cgroup1_memory_limits(char buf[static 1024], uint64_t* high,
                                          uint64_t* max) {
  char filename[4097];
  char* p;
  int n;
  uint64_t cgroup1_max;

  /* Find out where the controller is mounted. */
  p = uv__cgroup1_find_memory_controller(buf, &n);
  if (p != NULL) {
    snprintf(filename, sizeof(filename),
             "/sys/fs/cgroup/memory/%.*s/memory.soft_limit_in_bytes", n, p);
    *high = uv__read_uint64(filename);

    snprintf(filename, sizeof(filename),
             "/sys/fs/cgroup/memory/%.*s/memory.limit_in_bytes", n, p);
    *max = uv__read_uint64(filename);

    /* If the controller wasn't mounted, the reads above will have failed,
     * as indicated by uv__read_uint64 returning 0.
     */
     if (*high != 0 && *max != 0)
       goto update_limits;
  }

  /* Fall back to the limits of the global memory controller. */
  *high = uv__read_uint64("/sys/fs/cgroup/memory/memory.soft_limit_in_bytes");
  *max = uv__read_uint64("/sys/fs/cgroup/memory/memory.limit_in_bytes");

  /* uv__read_uint64 detects cgroup2's "max", so we need to separately detect
   * cgroup1's maximum value (which is derived from LONG_MAX and PAGE_SIZE).
   */
update_limits:
  cgroup1_max = LONG_MAX & ~(sysconf(_SC_PAGESIZE) - 1);
  if (*high == cgroup1_max)
    *high = UINT64_MAX;
  if (*max == cgroup1_max)
    *max = UINT64_MAX;
}

static void uv__get_cgroup2_memory_limits(char buf[static 1024], uint64_t* high,
                                          uint64_t* max) {
  char filename[4097];
  char* p;
  int n;

  /* Find out where the controller is mounted. */
  p = buf + strlen("0::/");
  n = (int) strcspn(p, "\n");

  /* Read the memory limits of the controller. */
  snprintf(filename, sizeof(filename), "/sys/fs/cgroup/%.*s/memory.max", n, p);
  *max = uv__read_uint64(filename);
  snprintf(filename, sizeof(filename), "/sys/fs/cgroup/%.*s/memory.high", n, p);
  *high = uv__read_uint64(filename);
}

static uint64_t uv__get_cgroup_constrained_memory(char buf[static 1024]) {
  uint64_t high;
  uint64_t max;

  /* In the case of cgroupv2, we'll only have a single entry. */
  if (strncmp(buf, "0::/", 4))
    uv__get_cgroup1_memory_limits(buf, &high, &max);
  else
    uv__get_cgroup2_memory_limits(buf, &high, &max);

  if (high == 0 || max == 0)
    return 0;

  return high < max ? high : max;
}

uint64_t uv_get_constrained_memory(void) {
  char buf[1024];
  uint64_t cgroup_limit;
  uint64_t rlimit_limit;

  cgroup_limit = 0;
  if (uv__slurp("/proc/self/cgroup", buf, sizeof(buf)) == 0)
    cgroup_limit = uv__get_cgroup_constrained_memory(buf);
  rlimit_limit = uv__get_rlimit_max_memory();

  /* Return the minimum of cgroup and rlimit constraints. */
  if (cgroup_limit == 0)
    return rlimit_limit;
  if (rlimit_limit == 0)
    return cgroup_limit;
  return cgroup_limit < rlimit_limit ? cgroup_limit : rlimit_limit;
}


static uint64_t uv__get_cgroup1_current_memory(char buf[static 1024]) {
  char filename[4097];
  uint64_t current;
  char* p;
  int n;

  /* Find out where the controller is mounted. */
  p = uv__cgroup1_find_memory_controller(buf, &n);
  if (p != NULL) {
    snprintf(filename, sizeof(filename),
            "/sys/fs/cgroup/memory/%.*s/memory.usage_in_bytes", n, p);
    current = uv__read_uint64(filename);

    /* If the controller wasn't mounted, the reads above will have failed,
     * as indicated by uv__read_uint64 returning 0.
     */
    if (current != 0)
      return current;
  }

  /* Fall back to the usage of the global memory controller. */
  return uv__read_uint64("/sys/fs/cgroup/memory/memory.usage_in_bytes");
}

static uint64_t uv__get_cgroup2_current_memory(char buf[static 1024]) {
  char filename[4097];
  char* p;
  int n;

  /* Find out where the controller is mounted. */
  p = buf + strlen("0::/");
  n = (int) strcspn(p, "\n");

  snprintf(filename, sizeof(filename),
           "/sys/fs/cgroup/%.*s/memory.current", n, p);
  return uv__read_uint64(filename);
}

uint64_t uv_get_available_memory(void) {
  char buf[1024];
  uint64_t constrained;
  uint64_t current;
  uint64_t total;

  if (uv__slurp("/proc/self/cgroup", buf, sizeof(buf)))
    return 0;

  constrained = uv__get_cgroup_constrained_memory(buf);
  if (constrained == 0)
    return uv_get_free_memory();

  total = uv_get_total_memory();
  if (constrained > total)
    return uv_get_free_memory();

  /* In the case of cgroupv2, we'll only have a single entry. */
  if (strncmp(buf, "0::/", 4))
    current = uv__get_cgroup1_current_memory(buf);
  else
    current = uv__get_cgroup2_current_memory(buf);

  /* memory usage can be higher than the limit (for short bursts of time) */
  if (constrained < current)
    return 0;

  return constrained - current;
}



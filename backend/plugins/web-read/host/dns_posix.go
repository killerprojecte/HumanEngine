//go:build web_read_candidate && cgo && (linux || darwin || freebsd || openbsd || netbsd || dragonfly)

package host

/*
#include <sys/types.h>
#include <sys/socket.h>
#include <netdb.h>
#include <netinet/in.h>
#include <stdlib.h>
#include <string.h>

// Exactly the hints used by Node's default dns.lookup(host, {all:true}).
// This is the blocking libc boundary itself, not an asynchronous Go resolver.
static int he_dns_lookup(const char *name, struct addrinfo **out) {
 struct addrinfo hints;
 memset(&hints, 0, sizeof(hints));
 hints.ai_family = AF_UNSPEC;
 hints.ai_socktype = SOCK_STREAM;
 return getaddrinfo(name, NULL, &hints, out);
}
static int he_dns_address(const struct addrinfo *ai, unsigned char *out) {
 if (ai->ai_family == AF_INET) {
  memcpy(out, &((struct sockaddr_in *)ai->ai_addr)->sin_addr, 4);
  return 4;
 }
 if (ai->ai_family == AF_INET6) {
  memcpy(out, &((struct sockaddr_in6 *)ai->ai_addr)->sin6_addr, 16);
  return 16;
 }
 return 0;
}
*/
import "C"

import (
	"errors"
	"strings"
	"unsafe"
)

const osDNSAvailable = true

func lookupOS(hostname string) DNSResult {
	// C strings cannot represent embedded NUL. URL policy already excludes this;
	// do not silently resolve a different truncated hostname through the raw port.
	if strings.IndexByte(hostname, 0) >= 0 {
		return DNSResult{Err: errors.New("invalid_dns_hostname")}
	}
	// libuv 1.52.1 uses a 256-byte IDNA output buffer including its NUL.
	// networkPrepare has already produced the ASCII URL hostname; at this port
	// 256 or more bytes cannot fit, even if the URL parser accepts the hostname.
	// Preserve that pre-libc failure, not a new DNS-label or URL-length policy.
	if len(hostname) >= 256 {
		return DNSResult{Err: errors.New("os_dns_hostname_buffer_exceeded")}
	}
	name := C.CString(hostname)
	defer C.free(unsafe.Pointer(name))
	var head *C.struct_addrinfo
	code := C.he_dns_lookup(name, &head)
	if head != nil {
		defer C.freeaddrinfo(head)
	}
	// Source discards all lookup error details and returns media_dns_failed.
	// Do not leak platform-dependent gai_strerror text or hostname in that path.
	if code != 0 {
		return DNSResult{Err: errors.New("os_dns_lookup_failed")}
	}
	out := DNSResult{}
	for current := head; current != nil; current = current.ai_next {
		var raw [16]byte
		size := int(C.he_dns_address(current, (*C.uchar)(unsafe.Pointer(&raw[0]))))
		if size == 0 {
			continue
		}
		if address, ok := nativeDNSAddress(raw[:size]); ok {
			out.Addresses = append(out.Addresses, address)
		}
	}
	// Node's AfterGetAddrInfo turns a successful empty native list into EAI_NODATA.
	if len(out.Addresses) == 0 {
		out.Err = errors.New("os_dns_no_data")
	}
	return out // deferred native frees run before Lookup can publish or drain
}

//go:build web_read_candidate && cgo && (linux || darwin || freebsd || openbsd || netbsd || dragonfly)

package host

import (
	"context"
	"encoding/json"
	"net/netip"
	"os/exec"
	"reflect"
	"testing"
)

func TestOSDNSNativeNumeric(t *testing.T) {
	r, err := NewOSResolver()
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAndWait()
	for _, name := range []string{"127.0.0.1", "::1"} {
		job := r.Lookup(context.Background(), name)
		requireDNSDrain(t, job)
		result := <-job.Result
		if result.Err != nil || len(result.Addresses) != 1 || result.Addresses[0] != netip.MustParseAddr(name) {
			t.Fatal(name, result)
		}
	}
	job := r.Lookup(context.Background(), "localhost\x00different.invalid")
	requireDNSDrain(t, job)
	if result := <-job.Result; result.Err == nil {
		t.Fatal("NUL was silently truncated")
	}
}

func TestOSDNSNativeNodeLocalhost(t *testing.T) {
	// Verification dependency only: the adapter does not launch or require Node.
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node oracle unavailable")
	}
	raw, err := exec.Command(node, "--input-type=module", "-e", `import {lookup} from 'node:dns/promises'; import {getDefaultResultOrder} from 'node:dns'; console.log(JSON.stringify({order:getDefaultResultOrder(),addresses:await lookup('localhost',{all:true})}));`).Output()
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Order     string
		Addresses []struct {
			Address string
			Family  int
		}
	}
	if err = json.Unmarshal(raw, &oracle); err != nil {
		t.Fatal(err)
	}
	if oracle.Order != "verbatim" {
		t.Fatal("oracle runtime overrides source default order", oracle.Order)
	}
	r, err := NewOSResolver()
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAndWait()
	job := r.Lookup(context.Background(), "localhost")
	requireDNSDrain(t, job)
	result := <-job.Result
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	var want []netip.Addr
	for _, a := range oracle.Addresses {
		ip, err := netip.ParseAddr(a.Address)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, ip)
	}
	if !reflect.DeepEqual(result.Addresses, want) {
		t.Fatal("native order/family/duplicates differ", result.Addresses, want)
	}
}

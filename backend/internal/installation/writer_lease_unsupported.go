//go:build !linux

package installation

import "errors"

func acquireDatabaseLease(string) (*writerLease, error) {
	return nil, errors.New("installation_writer_lease_platform_unsupported")
}
func (*writerLease) acquireAuthority(string) error {
	return errors.New("installation_writer_lease_platform_unsupported")
}

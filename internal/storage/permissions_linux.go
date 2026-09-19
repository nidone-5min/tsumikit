package storage

import "golang.org/x/sys/unix"

func removeACL(path string) error {
	for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		err := unix.Removexattr(path, name)
		if err != nil && err != unix.ENODATA && err != unix.ENOTSUP {
			return err
		}
	}
	return nil
}

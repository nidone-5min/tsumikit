package storage

import (
	"context"
	"os/exec"
	"time"
)

// macOS ACL entries can grant access even with mode 0700/0600. Remove them
// with the system utility, without a shell or emitting its potentially private output.
func removeACL(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/bin/chmod", "-N", path).Run()
}

//go:build unix

package crawlercapture

import (
	"github.com/Laisky/errors/v2"
	"os"
	"syscall"
)

// checkOwner permits system ancestors and requires the private leaf owner.
func checkOwner(info os.FileInfo, leaf bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("journal ownership unavailable")
	}
	uid := uint32(os.Geteuid())
	if (leaf && stat.Uid != uid) || (!leaf && stat.Uid != uid && stat.Uid != 0) {
		return errors.New("journal path has untrusted owner")
	}
	return nil
}

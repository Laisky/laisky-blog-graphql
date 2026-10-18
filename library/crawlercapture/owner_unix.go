//go:build unix

package crawlercapture

import (
	"os"
	"syscall"

	"github.com/Laisky/errors/v2"
)

// checkOwner permits system ancestors and requires the private leaf owner.
func checkOwner(info os.FileInfo, leaf bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("journal ownership unavailable")
	}
	uid := int64(os.Geteuid())
	if (leaf && int64(stat.Uid) != uid) || (!leaf && int64(stat.Uid) != uid && stat.Uid != 0) {
		return errors.New("journal path has untrusted owner")
	}
	return nil
}

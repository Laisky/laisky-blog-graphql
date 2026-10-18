//go:build !unix

package crawlercapture

import (
	"github.com/Laisky/errors/v2"
	"os"
)

// checkOwner rejects platforms without the private Unix ownership contract.
func checkOwner(_ os.FileInfo, _ bool) error {
	return errors.New("private failure journal requires Unix ownership")
}

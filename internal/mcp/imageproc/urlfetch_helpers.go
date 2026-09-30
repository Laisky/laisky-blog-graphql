package imageproc

import "errors"

// isTimeout recognizes timeout errors through ordinary and joined causes.
func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

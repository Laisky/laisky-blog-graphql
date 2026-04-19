package userrequests

import "fmt"

// defaultSscanf is the production Sscanf wrapper. image_http.go routes
// extractAttachmentIndex through this function so unit tests can stub it
// deterministically without importing fmt there.
func defaultSscanf(s, format string, a ...any) (int, error) {
	return fmt.Sscanf(s, format, a...)
}

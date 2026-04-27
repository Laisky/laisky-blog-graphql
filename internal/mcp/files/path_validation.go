package files

import (
	"strings"
)

// ProjectWildcard expands a file_search call to every project owned by the authenticated user.
// It is accepted only by Service.Search; all other file operations reject it.
const ProjectWildcard = "*"

// ValidateProject verifies the project identifier against length and charset rules.
func ValidateProject(project string) error {
	if strings.TrimSpace(project) == "" {
		return NewError(ErrCodeInvalidPath, "project is required", false)
	}
	if len(project) > 128 {
		return NewError(ErrCodeInvalidPath, "project exceeds max length", false)
	}
	for i := 0; i < len(project); i++ {
		char := project[i]
		if char != '_' && char != '-' && char != '.' && !isASCIILetterOrDigit(char) {
			return NewError(ErrCodeInvalidPath, "project contains invalid characters", false)
		}
	}
	return nil
}

// ValidateSearchProject verifies the project identifier for file_search,
// additionally accepting ProjectWildcard ("*") for cross-project searches.
func ValidateSearchProject(project string) error {
	if project == ProjectWildcard {
		return nil
	}
	return ValidateProject(project)
}

// ValidatePath verifies a file path against PRD rules.
func ValidatePath(path string) error {
	if len(path) > 512 {
		return NewError(ErrCodeInvalidPath, "path exceeds max length", false)
	}
	if path == "" {
		return nil
	}
	if !strings.HasPrefix(path, "/") {
		return NewError(ErrCodeInvalidPath, "path must start with '/'", false)
	}
	if strings.HasSuffix(path, "/") {
		return NewError(ErrCodeInvalidPath, "path must not end with '/'", false)
	}
	if strings.Contains(path, "//") {
		return NewError(ErrCodeInvalidPath, "path must not contain empty segments", false)
	}
	segments := strings.Split(path, "/")
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		if seg == "." || seg == ".." {
			return NewError(ErrCodeInvalidPath, "path must not contain '.' or '..'", false)
		}
	}
	for i := 0; i < len(path); i++ {
		char := path[i]
		if char < 32 || char == 127 {
			return NewError(ErrCodeInvalidPath, "path must not contain control characters", false)
		}
		if char == ' ' || char == '\t' || char == '\n' || char == '\r' {
			return NewError(ErrCodeInvalidPath, "path must not contain whitespace", false)
		}
		if char != '/' && char != '_' && char != '-' && char != '.' && !isASCIILetterOrDigit(char) {
			return NewError(ErrCodeInvalidPath, "path contains invalid characters", false)
		}
	}
	return nil
}

// isASCIILetterOrDigit reports whether a byte is an ASCII letter or digit.
func isASCIILetterOrDigit(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9')
}

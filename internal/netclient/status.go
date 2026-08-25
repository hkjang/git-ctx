package netclient

import "fmt"

// HTTPStatusError carries the status from a non-2xx response without changing
// the integration-specific error text. Status deliberately matches the small
// interface consumed by source.StatusOf, avoiding a dependency from low-level
// HTTP clients back to the source package.
type HTTPStatusError struct {
	statusCode int
	cause      error
}

// NewHTTPStatusError annotates cause with an HTTP response status. Callers
// should construct their existing descriptive error first so operator-facing
// messages remain stable.
func NewHTTPStatusError(statusCode int, cause error) *HTTPStatusError {
	if cause == nil {
		cause = fmt.Errorf("HTTP %d", statusCode)
	}
	return &HTTPStatusError{statusCode: statusCode, cause: cause}
}

func (e *HTTPStatusError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *HTTPStatusError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *HTTPStatusError) Status() int {
	if e == nil {
		return 0
	}
	return e.statusCode
}

// Ensure the carrier keeps the standard wrapping contract even if its
// implementation is refactored later.
var _ interface {
	error
	Unwrap() error
	Status() int
} = (*HTTPStatusError)(nil)

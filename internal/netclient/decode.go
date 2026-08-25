package netclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// DecodeFailure explains a response body an integration could not read.
//
// encoding/json names the Go type it was decoding into, and that string travels
// all the way to an MCP client and the operations screen, where "[]gitlab.blobHit"
// tells the reader nothing. The causes worth checking are always the same: a
// base URL pointing at a proxy or a login page, an unsupported server version,
// or a gateway that wrapped the response in an envelope. The cause is therefore
// described in JSON terms, and the server and endpoint are named so the reader
// knows which integration to look at.
func DecodeFailure(server, api, endpoint string, err error) error {
	cause := "the body was not valid JSON"
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		expected := "a value"
		switch typeErr.Type.Kind() {
		case reflect.Slice, reflect.Array:
			expected = "a list"
		case reflect.Struct, reflect.Map:
			expected = "an object"
		case reflect.String:
			expected = "a string"
		case reflect.Bool:
			expected = "a boolean"
		}
		cause = fmt.Sprintf("the body was %s where %s was expected", typeErr.Value, expected)
	}
	return fmt.Errorf("%s returned a response for %s that this client could not read: %s. Check that the base URL points at the %s rather than a proxy or login page, and that the server version is supported",
		server, endpoint, cause, api)
}

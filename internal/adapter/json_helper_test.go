package adapter_test

import (
	"encoding/json"
	"io"
	"net/http"
)

// readJSON decodes the JSON body of an incoming request, used in test stubs.
func readJSON(r *http.Request) (map[string]any, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// jsonString quotes a string as a JSON value (with escaped quotes/newlines).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

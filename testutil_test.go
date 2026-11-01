package main

import "encoding/json"

// jsonQuote encodes s as a JSON string; Windows paths are full of
// backslashes that would otherwise be read as escapes.
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

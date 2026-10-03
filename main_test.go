package main

import "testing"

func TestParseSessionTTLRejectsOutOfRangeValues(t *testing.T) {
	for _, value := range []string{"14m", "169h"} {
		if _, err := parseSessionTTL(value); err == nil {
			t.Fatalf("parseSessionTTL(%q) unexpectedly succeeded", value)
		}
	}
}

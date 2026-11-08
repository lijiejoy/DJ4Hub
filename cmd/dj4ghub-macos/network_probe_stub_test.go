//go:build !darwin

package main

import "testing"

func TestCellularBindingRejectsUnsupportedPlatform(t *testing.T) {
	if err := bindCellularSocket(0, 1); err == nil {
		t.Fatal("unsupported interface binding must not silently use the default route")
	}
}

//go:build !darwin

package main

import "errors"

func bindCellularSocket(_ uintptr, _ int) error {
	return errors.New("cellular interface binding is only supported on macOS")
}

//go:build !darwin

package main

import (
	"errors"
	"io"
)

func manageService(string, serviceOptions, io.Writer) error {
	return errors.New("tp service requires macOS; use tp serve on this platform")
}

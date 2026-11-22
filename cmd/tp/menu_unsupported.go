//go:build !darwin

package main

import (
	"errors"
	"io"
)

func setupMenu(_ string, _ io.Reader, _, _ io.Writer) error {
	return errors.New("the Teleport menu bar is available on macOS")
}

func startMenu() error {
	return errors.New("the Teleport menu bar is available on macOS")
}

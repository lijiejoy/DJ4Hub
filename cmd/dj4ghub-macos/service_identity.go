package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

// Capture before an app upgrade can replace the executable on disk.
var serviceIdentity = readServiceIdentity()

type serviceProcessIdentity struct {
	PID        int    `json:"pid"`
	Executable string `json:"executable"`
	SHA256     string `json:"sha256"`
}

func readServiceIdentity() serviceProcessIdentity {
	identity := serviceProcessIdentity{PID: os.Getpid()}
	path, err := os.Executable()
	if err != nil {
		return identity
	}
	identity.Executable = path
	file, err := os.Open(path)
	if err != nil {
		return identity
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err == nil {
		identity.SHA256 = fmt.Sprintf("%x", hash.Sum(nil))
	}
	return identity
}

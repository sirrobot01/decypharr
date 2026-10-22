package server

import (
	"crypto/sha256"
	"embed"
	"fmt"
)

//go:embed assets/build/*
var assetsEmbed embed.FS

// Content hashes keep cached assets in sync with templates after an upgrade.
func assetVersion(path string) (string, error) {
	data, err := assetsEmbed.ReadFile("assets/build/" + path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

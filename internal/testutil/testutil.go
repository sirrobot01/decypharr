package testutil

import (
	"os"
	"path/filepath"
	"strings"
)

// GetTestDataPath returns the path to the testdata directory in the project root
func GetTestDataPath() string {
	return filepath.Join("..", "..", "testdata")
}

// GetTestDataFilePath returns the path to a specific file in the testdata directory
func GetTestDataFilePath(filename string) string {
	return filepath.Join(GetTestDataPath(), filename)
}

// GetTestTorrentPath returns the path to the Ubuntu test torrent file
func GetTestTorrentPath() string {
	return GetTestDataFilePath("ubuntu-25.04-desktop-amd64.iso.torrent")
}

// GetTestMagnetPath returns the path to the Ubuntu test magnet file
func GetTestMagnetPath() string {
	return GetTestDataFilePath("ubuntu-25.04-desktop-amd64.iso.magnet")
}

// GetTestDataBytes reads and returns the raw bytes of a test data file
func GetTestDataBytes(filename string) ([]byte, error) {
	filePath := GetTestDataFilePath(filename)
	return os.ReadFile(filePath)
}

// GetTestDataContent reads and returns the content of a test data file
func GetTestDataContent(filename string) (string, error) {

	content, err := GetTestDataBytes(filename)
	return strings.TrimSpace(string(content)), err
}

// GetTestMagnetContent reads and returns the content of the Ubuntu test magnet file
func GetTestMagnetContent() (string, error) {
	return GetTestDataContent("ubuntu-25.04-desktop-amd64.iso.magnet")
}

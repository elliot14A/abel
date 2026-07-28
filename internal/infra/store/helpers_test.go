package store_test

import (
	"os"
	"strings"
)

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

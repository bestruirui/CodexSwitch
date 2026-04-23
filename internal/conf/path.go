package conf

import (
	"os"
	"path/filepath"
)

func DataPath(parts ...string) string {
	filePath, err := os.Executable()
	if err != nil {
		return filepath.Join(append([]string{"data"}, parts...)...)
	}
	if resolvedPath, resolveErr := filepath.EvalSymlinks(filePath); resolveErr == nil {
		filePath = resolvedPath
	}
	return filepath.Join(append([]string{filepath.Dir(filePath), "data"}, parts...)...)
}

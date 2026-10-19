package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

func BaseDir() (string, error) {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "netpair"), nil
	}
	uid := os.Getuid()
	return filepath.Join("/run/user", fmt.Sprintf("%d", uid), "netpair"), nil
}

func PairDir(name string) (string, error) {
	base, err := BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, name), nil
}

func SocketPath(name string) (string, error) {
	base, err := BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, name+".sock"), nil
}

func StatePath(name string) (string, error) {
	base, err := BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, name+".json"), nil
}

func EnsureBase() error {
	base, err := BaseDir()
	if err != nil {
		return err
	}
	return os.MkdirAll(base, 0o700)
}

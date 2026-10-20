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

// LogPath returns the path of the pasta log file for a pair. pasta logs to
// this file (instead of stderr) so failures remain inspectable after create.
func LogPath(name string) (string, error) {
	base, err := BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, name+".log"), nil
}

// ResolvPath returns the path of the generated resolver configuration for a
// pair. The manager bind-mounts it over /etc/resolv.conf inside the pair's
// mount namespace (pasta copies the host ones, which may point at the
// systemd stub, unreachable from the namespace).
func ResolvPath(name string) (string, error) {
	base, err := BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, name+".resolv.conf"), nil
}

func EnsureBase() error {
	base, err := BaseDir()
	if err != nil {
		return err
	}
	return os.MkdirAll(base, 0o700)
}

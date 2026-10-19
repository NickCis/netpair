package sshclient

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

type Config struct {
	User            string
	Host            string
	Port            int
	IdentityFile    string
	InsecureHostKey bool
}

func (c Config) addr() string {
	port := c.Port
	if port == 0 {
		port = 22
	}
	return fmt.Sprintf("%s:%d", c.Host, port)
}

func Dial(cfg Config) (*ssh.Client, error) {
	auth, err := buildAuthMethods(cfg)
	if err != nil {
		return nil, err
	}
	hostKeyCallback, err := hostKeyCallback(cfg)
	if err != nil {
		return nil, err
	}
	return ssh.Dial("tcp", cfg.addr(), &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
		Timeout:         15 * time.Second,
	})
}

func buildAuthMethods(cfg Config) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	keys, err := loadPrivateKeys(cfg.IdentityFile)
	if err != nil {
		return nil, err
	}
	for _, s := range keys {
		methods = append(methods, ssh.PublicKeys(s))
	}
	methods = append(methods, ssh.PasswordCallback(func() (string, error) {
		fmt.Fprintf(os.Stderr, "Password for %s@%s: ", cfg.User, cfg.Host)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}))
	methods = append(methods, ssh.KeyboardInteractive(func(_ string, _ string, questions []string, echos []bool) ([]string, error) {
		var answers []string
		for i, q := range questions {
			echo := true
			if i < len(echos) {
				echo = echos[i]
			}
			fmt.Fprintf(os.Stderr, "%s", q)
			if !echo {
				b, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
				if err != nil {
					return nil, err
				}
				answers = append(answers, string(b))
			} else {
				line, err := bufio.NewReader(os.Stdin).ReadString('\n')
				if err != nil {
					return nil, err
				}
				answers = append(answers, strings.TrimSpace(line))
			}
		}
		return answers, nil
	}))
	return methods, nil
}

func loadPrivateKeys(identityFile string) ([]ssh.Signer, error) {
	var paths []string
	if identityFile != "" {
		paths = []string{identityFile}
	} else {
		home, _ := os.UserHomeDir()
		for _, n := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
			p := filepath.Join(home, ".ssh", n)
			if _, err := os.Stat(p); err == nil {
				paths = append(paths, p)
			}
		}
	}
	var signers []ssh.Signer
	for _, p := range paths {
		key, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Passphrase for %s: ", p)
			pass, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				continue
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, pass)
			if err != nil {
				continue
			}
		}
		signers = append(signers, signer)
	}
	return signers, nil
}

func hostKeyCallback(cfg Config) (ssh.HostKeyCallback, error) {
	if cfg.InsecureHostKey {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	home, _ := os.UserHomeDir()
	knownHosts := filepath.Join(home, ".ssh", "known_hosts")
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		hostKey := fmt.Sprintf("[%s]:%d", cfg.Host, cfg.Port)
		if cfg.Port == 0 || cfg.Port == 22 {
			hostKey = cfg.Host
		}
		f, err := os.Open(knownHosts)
		if err != nil {
			return promptHostKey(hostKey, key, knownHosts)
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			if fields[0] != hostKey && fields[0] != cfg.Host && fields[0] != hostname {
				continue
			}
			known, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.Join(fields[2:], " ")))
			if err != nil {
				continue
			}
			if ssh.FingerprintSHA256(known) == ssh.FingerprintSHA256(key) {
				return nil
			}
			return fmt.Errorf("host key mismatch for %s", hostKey)
		}
		return promptHostKey(hostKey, key, knownHosts)
	}, nil
}

func promptHostKey(host string, key ssh.PublicKey, path string) error {
	fmt.Fprintf(os.Stderr, "The authenticity of host %s can't be established.\n", host)
	fmt.Fprintf(os.Stderr, "SHA256:%s.\n", ssh.FingerprintSHA256(key))
	fmt.Fprintf(os.Stderr, "Are you sure you want to continue connecting (yes/no)? ")
	var ans string
	fmt.Scanln(&ans)
	if strings.ToLower(strings.TrimSpace(ans)) != "yes" {
		return fmt.Errorf("host key verification failed")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s %s %s\n", host, key.Type(), ssh.MarshalAuthorizedKey(key))
	return err
}

func ParseRemote(spec string) (user, host string, err error) {
	at := strings.LastIndex(spec, "@")
	if at <= 0 {
		return "", "", fmt.Errorf("invalid remote %q, expected user@host", spec)
	}
	return spec[:at], spec[at+1:], nil
}

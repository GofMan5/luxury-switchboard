package openssh

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/atomicfile"
)

const (
	Host    = "81.90.28.122"
	HostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBRmk/YVgGOza+PkmT91qaX7D1vTF3YhVOlBzRnxAsEy"
)

type Client struct {
	Executable string
	Identity   string
	KnownHosts string
}

func Prepare(identityName string) (Client, error) {
	executable := ""
	if runtime.GOOS == "windows" {
		executable = filepath.Join(os.Getenv("WINDIR"), "System32", "OpenSSH", "ssh.exe")
		if info, err := os.Stat(executable); err != nil || info.IsDir() {
			return Client{}, errors.New("SSH client is not installed")
		}
	} else {
		var err error
		executable, err = exec.LookPath("ssh")
		if err != nil {
			return Client{}, errors.New("SSH client is not installed")
		}
	}
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return Client{}, errors.New("SSH identity directory is unavailable")
	}
	identity := filepath.Join(root, "ProviderSwitchboard", "ssh", identityName)
	if info, err := os.Stat(identity); err != nil || info.IsDir() {
		return Client{}, errors.New("SSH identity is not installed")
	}
	knownHosts := filepath.Join(filepath.Dir(identity), "model-tunnel_known_hosts")
	if err := ensureKnownHosts(knownHosts); err != nil {
		return Client{}, errors.New("SSH host trust could not be prepared")
	}
	return Client{Executable: executable, Identity: identity, KnownHosts: knownHosts}, nil
}

func ensureKnownHosts(path string) error {
	payload := []byte(Host + " " + HostKey + "\n")
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, payload) {
		return nil
	}
	return atomicfile.Replace(path, payload, 0o600)
}

func (client Client) BaseArgs() []string {
	null := "/dev/null"
	if runtime.GOOS == "windows" {
		null = "NUL"
	}
	return []string{
		"-T", "-F", null, "-i", client.Identity,
		"-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "LogLevel=ERROR", "-o", "ForwardAgent=no", "-o", "ForwardX11=no",
		"-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + client.KnownHosts, "-o", "GlobalKnownHostsFile=" + null,
		"-o", "HostKeyAlgorithms=ssh-ed25519", "-o", "UpdateHostKeys=no",
		"-o", "IdentityAgent=none", "-o", "PubkeyAuthentication=yes",
		"-o", "PreferredAuthentications=publickey", "-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no", "-o", "ChallengeResponseAuthentication=no",
		"-o", "PermitLocalCommand=no", "-o", "RequestTTY=no",
	}
}

func Configure(command *exec.Cmd) {
	hideCommand(command)
	command.Env = childEnvironment()
	command.Stdin = nil
	command.Stdout = io.Discard
}

func childEnvironment() []string {
	allowed := map[string]struct{}{"SYSTEMROOT": {}, "WINDIR": {}, "TEMP": {}, "TMP": {}, "USERPROFILE": {}, "LOCALAPPDATA": {}, "PROGRAMDATA": {}, "HOMEDRIVE": {}, "HOMEPATH": {}, "USERNAME": {}, "USERDOMAIN": {}}
	result := make([]string, 0, len(allowed))
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if _, ok := allowed[strings.ToUpper(name)]; ok {
			result = append(result, value)
		}
	}
	return result
}

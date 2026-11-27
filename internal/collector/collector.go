package collector

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Host struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type Settings struct {
	Hosts    []Host `json:"hosts"`
	User     string `json:"user"`
	Gateway  string `json:"gateway"`
	LogDir   string `json:"log_dir"`
	LogGlob  string `json:"log_glob"`
	Password string `json:"password,omitempty"`
}

var (
	hostName    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	dnsName     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]{0,252}$`)
	userName    = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	logDir      = regexp.MustCompile(`^/[a-zA-Z0-9_./-]+$`)
	logGlob     = regexp.MustCompile(`^[a-zA-Z0-9_.*?-]{1,120}$`)
	password    = regexp.MustCompile(`^[a-fA-F0-9]{32,128}$`)
	portPattern = regexp.MustCompile(`^[0-9]{1,5}$`)
)

func reservedHost(name string) bool {
	switch strings.ToLower(name) {
	case "localhost", "all", "ungrouped", "nginx_collectors":
		return true
	}
	return false
}

func (s Settings) Validate() error {
	if len(s.Hosts) < 1 || len(s.Hosts) > 50 || !userName.MatchString(s.User) {
		return errors.New("configure one to fifty hosts and a valid SSH user")
	}
	names := make(map[string]bool, len(s.Hosts))
	targets := make(map[string]bool, len(s.Hosts))
	for _, host := range s.Hosts {
		target := net.JoinHostPort(host.Address, strconv.Itoa(host.Port))
		if !hostName.MatchString(host.Name) || reservedHost(host.Name) || names[host.Name] || targets[target] ||
			(net.ParseIP(host.Address) == nil && !dnsName.MatchString(host.Address)) || host.Port < 1 || host.Port > 65535 {
			return errors.New("invalid or duplicate collector host")
		}
		names[host.Name], targets[target] = true, true
	}
	u, err := url.Parse(s.Gateway)
	if err != nil || !strings.HasPrefix(s.Gateway, "https://") || u.Host == "" || u.User != nil ||
		s.Gateway != "https://"+u.Host || !dnsName.MatchString(u.Hostname()) {
		return errors.New("gateway must be an HTTPS origin without path")
	}
	if p := u.Port(); p != "" {
		n, _ := strconv.Atoi(p)
		if !portPattern.MatchString(p) || n < 1 || n > 65535 {
			return errors.New("invalid gateway port")
		}
	}
	if !logDir.MatchString(s.LogDir) || filepath.Clean(s.LogDir) != s.LogDir || !logGlob.MatchString(s.LogGlob) ||
		s.LogGlob == "." || s.LogGlob == ".." || !password.MatchString(s.Password) {
		return errors.New("invalid log path, glob, or ingest password")
	}
	return nil
}

const Timeout = 6 * time.Minute

type Runner struct {
	Binary, PlaybookDir, KnownHosts, PrivateKey, CAFile string
}

func (r Runner) Enabled() bool {
	return r.Binary != "" && r.PlaybookDir != "" && r.KnownHosts != "" && r.PrivateKey != ""
}

func (r Runner) Run(ctx context.Context, action string, settings Settings) error {
	if !r.Enabled() {
		return errors.New("collector runner is disabled")
	}
	playbook := "check.yml"
	switch action {
	case "check":
	case "deploy":
		playbook = "filebeat.yml"
	default:
		return errors.New("invalid collector action")
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "aiobs-collector-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	hosts := make(map[string]map[string]any, len(settings.Hosts))
	for _, host := range settings.Hosts {
		hosts[host.Name] = map[string]any{
			"ansible_host": host.Address, "ansible_port": host.Port,
			"ansible_user": settings.User, "collector_hostname": host.Name,
		}
	}
	inventory := map[string]any{"all": map[string]any{"children": map[string]any{
		"nginx_collectors": map[string]any{"hosts": hosts},
	}}}
	vars := map[string]string{
		"collector_elasticsearch_url":      settings.Gateway,
		"collector_elasticsearch_password": settings.Password,
		"collector_log_dir":                settings.LogDir,
		"collector_log_glob":               settings.LogGlob,
	}
	if r.CAFile != "" {
		vars["collector_ca_file"] = r.CAFile
	}
	for name, value := range map[string]any{"inventory.json": inventory, "vars.json": vars} {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			return err
		}
	}
	// Keep deployment credentials and the rest of the service environment out of Ansible.
	cfg := "[defaults]\nhost_key_checking = True\nretry_files_enabled = False\n"
	if err := os.WriteFile(filepath.Join(dir, "ansible.cfg"), []byte(cfg), 0600); err != nil {
		return err
	}
	// Copy the allowlist into a private directory with a path safe for SSH argument parsing.
	knownHosts, err := os.ReadFile(r.KnownHosts)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "known_hosts"), knownHosts, 0600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, r.Binary,
		"-i", filepath.Join(dir, "inventory.json"), "--extra-vars", "@"+filepath.Join(dir, "vars.json"),
		"--private-key", r.PrivateKey, "--forks", "5", "--timeout", "8",
		"--ssh-common-args", "-F /dev/null -o BatchMode=yes -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o UserKnownHostsFile="+filepath.Join(dir, "known_hosts"),
		filepath.Join(r.PlaybookDir, playbook))
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + dir, "LANG=C.UTF-8", "ANSIBLE_CONFIG=" + filepath.Join(dir, "ansible.cfg")}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return cmd.Run()
}

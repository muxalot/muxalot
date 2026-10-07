package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	installBin  = "/usr/local/bin/muxalot-agent"
	installUnit = "/etc/systemd/system/muxalot-agent.service"
)

func unitFile(user, listen, filesRoot string) string {
	return fmt.Sprintf(`[Unit]
Description=muxalot-agent remote terminal agent
# Wait for real connectivity: the agent binds a specific IP, which may only
# exist once DHCP has run. plain network.target can fire earlier.
Wants=network-online.target
After=network-online.target network.target

[Service]
# Unprivileged user only; the agent refuses to run as root.
User=%[1]s
Group=%[1]s
ExecStart=%[4]s serve --listen %[2]s --files-root %[3]s
Restart=on-failure
# Deliberately NOT sandboxed with ProtectSystem/NoNewPrivileges: this is an interactive
# shell, so tmux needs /tmp and users may need sudo. Limit access with the Unix user instead.

[Install]
WantedBy=multi-user.target
`, user, listen, filesRoot, installBin)
}

// proxyConfig returns a reverse-proxy snippet for the agent. Every variant must
// keep the request path untouched (the signature covers it), pass WebSocket
// upgrades, send X-Forwarded-For, and not buffer file streams.
func proxyConfig(kind, domain, upstream string) (string, error) {
	switch kind {
	case "caddy":
		return fmt.Sprintf(`%s {
	reverse_proxy %s {
		flush_interval -1
	}
	request_body {
		max_size 2GB
	}
}
`, domain, upstream), nil
	case "nginx":
		return fmt.Sprintf(`server {
	listen 443 ssl;
	server_name %s;
	# ssl_certificate and ssl_certificate_key: add your certificate paths
	client_max_body_size 2g;
	location / {
		proxy_pass http://%s;
		proxy_http_version 1.1;
		proxy_set_header Upgrade $http_upgrade;
		proxy_set_header Connection "upgrade";
		proxy_set_header Host $http_host;
		proxy_set_header X-Forwarded-For $remote_addr;
		proxy_buffering off;
		proxy_request_buffering off;
		proxy_read_timeout 1h;
	}
}
`, domain, upstream), nil
	case "apache":
		return fmt.Sprintf(`# needs: a2enmod proxy proxy_http proxy_wstunnel rewrite ssl
<VirtualHost *:443>
	ServerName %[1]s
	# SSLEngine on, SSLCertificateFile, SSLCertificateKeyFile: add your certificate paths
	ProxyRequests Off
	ProxyPreserveHost On
	RewriteEngine On
	RewriteCond %%{HTTP:Upgrade} =websocket [NC]
	RewriteRule /(.*) ws://%[2]s/$1 [P,L]
	ProxyPass / http://%[2]s/
	ProxyPassReverse / http://%[2]s/
	LimitRequestBody 0
</VirtualHost>
`, domain, upstream), nil
	}
	return "", fmt.Errorf("unknown proxy type %q (use caddy, nginx or apache)", kind)
}

// installDeps are the commands the installer and the running agent rely on.
var installDeps = []string{"systemctl", "useradd", "tmux"}

func missingDeps(look func(string) (string, error)) []string {
	var missing []string
	for _, d := range installDeps {
		if _, err := look(d); err != nil {
			missing = append(missing, d)
		}
	}
	return missing
}

// defaultServiceUser is the user who invoked sudo, else the dedicated account.
func defaultServiceUser() string {
	if u := os.Getenv("SUDO_USER"); u != "" && u != "root" {
		return u
	}
	return "muxalot-agent"
}

// askUser prompts for the service user, discouraging root (which the agent refuses anyway).
func askUser(def string, in io.Reader, out io.Writer) string {
	r := bufio.NewReader(in)
	for {
		fmt.Fprintf(out, `Run the agent as which Unix user? It and every terminal you open will have this
user's full access. A dedicated or your own account is safer than root; root is not allowed.
User [%s]: `, def)
		line, err := r.ReadString('\n')
		u := strings.TrimSpace(line)
		if u == "" {
			u = def
		}
		if u != "root" {
			return u
		}
		fmt.Fprintln(out, "root is not allowed: the agent refuses to run as root.")
		if err != nil {
			return def
		}
	}
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runInstall copies this binary to installBin, creates the service user and
// installs and starts the systemd unit. Needs root.
func runInstall(user, listen, filesRoot string) {
	if os.Getuid() != 0 {
		log.Fatal("install must run as root, e.g. sudo muxalot-agent install")
	}
	if m := missingDeps(exec.LookPath); len(m) > 0 {
		log.Fatalf("missing required commands: %s", strings.Join(m, ", "))
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		log.Fatal("systemd is not running on this machine; run the agent yourself instead: muxalot-agent serve")
	}
	if user == "" {
		user = defaultServiceUser()
		// stdin is the curl pipe under `curl | sudo sh`, so ask on the terminal
		if tty, err := os.Open("/dev/tty"); err == nil {
			user = askUser(user, tty, os.Stdout)
			tty.Close()
		}
	}
	if user == "root" {
		log.Fatal("refusing to install as root; use an unprivileged user")
	}
	if filesRoot == "" {
		filesRoot = "/home/" + user
	}
	self, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		log.Fatal(err)
	}
	if self != installBin {
		if err := os.WriteFile(installBin, b, 0o755); err != nil {
			log.Fatal(err)
		}
	}
	if exec.Command("id", "-u", user).Run() != nil {
		if err := run("useradd", "-m", user); err != nil {
			log.Fatal(err)
		}
	}
	if err := os.WriteFile(installUnit, []byte(unitFile(user, listen, filesRoot)), 0o644); err != nil {
		log.Fatal(err)
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", "muxalot-agent"}} {
		if err := run("systemctl", args...); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Printf(`installed %s and started muxalot-agent as user %q on %s

next:
  1. put a TLS reverse proxy in front:  muxalot-agent proxy --type caddy|nginx|apache --domain your.host
  2. pair your phone:                   sudo -u %s %s pair --url https://your.host
`, filepath.Base(installBin), user, listen, user, installBin)
}

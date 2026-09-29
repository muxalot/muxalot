package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

func usage() {
	fmt.Fprint(os.Stderr, `muxalot-agent - remote terminal agent (run behind a TLS reverse proxy such as Caddy)

Usage:
  muxalot-agent serve   [--listen 127.0.0.1:8787] [--data DIR] [--files-root DIR] [--max-upload-mb 2048]
  muxalot-agent pair    --url https://tty.example.com [--data DIR]   print QR + one-time code
  muxalot-agent devices [--data DIR]                                  list paired devices
  muxalot-agent add-key --name NAME (PUBKEY_B64 | @FILE) [--data DIR]  register a public key directly
  muxalot-agent revoke  ID [--data DIR]                               revoke a device
`)
	os.Exit(2)
}

func defaultDataDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "muxalot-agent")
	}
	return ".muxalot-agent"
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	data := fs.String("data", defaultDataDir(), "state directory")
	listen := fs.String("listen", "127.0.0.1:8787", "listen address (keep on loopback; proxy handles TLS)")
	home, _ := os.UserHomeDir()
	root := fs.String("files-root", home, "directory tree exposed to upload/download")
	maxUp := fs.Int64("max-upload-mb", 2048, "max upload size in MiB")
	name := fs.String("name", "", "device name (add-key)")
	pubURL := fs.String("url", "", "public https URL of this server (for pair)")

	// allow the positional ID for `revoke` before/after flags
	var positional []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			os.Exit(2)
		}
		args = fs.Args()
		if len(args) > 0 {
			positional = append(positional, args[0])
			args = args[1:]
		}
	}

	st, err := NewStore(*data)
	if err != nil {
		log.Fatal(err)
	}

	switch cmd {
	case "serve":
		if os.Getuid() == 0 {
			log.Fatal("refusing to run as root; use an unprivileged user")
		}
		srv, err := NewServer(st, *root, *maxUp)
		if err != nil {
			log.Fatal(err)
		}
		hs := &http.Server{
			Addr:              *listen,
			Handler:           srv.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		log.Printf("listening on %s (files root %s)", *listen, srv.filesRoot)
		log.Fatal(hs.ListenAndServe())

	case "pair":
		if *pubURL == "" {
			log.Fatal("--url is required, e.g. --url https://tty.example.com")
		}
		u, err := url.Parse(*pubURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			log.Fatal("--url must be an https URL")
		}
		code, err := st.NewPairCode()
		if err != nil {
			log.Fatal(err)
		}
		payload := fmt.Sprintf("muxalot://pair?url=%s&code=%s",
			url.QueryEscape(strings.TrimRight(*pubURL, "/")), code)
		q, err := qrcode.New(payload, qrcode.Medium)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(q.ToSmallString(false))
		fmt.Printf("Server: %s\nCode:   %s   (valid %d minutes, single use)\n", *pubURL, code, int(pairCodeTTL.Minutes()))

	case "devices":
		devs, err := st.Devices()
		if err != nil {
			log.Fatal(err)
		}
		if len(devs) == 0 {
			fmt.Println("no paired devices")
		}
		for _, d := range devs {
			fmt.Printf("%s  %-20s  paired %s  last seen %s\n", d.ID, d.Name,
				d.Created.Format("2006-01-02"), d.LastSeen.Format("2006-01-02 15:04"))
		}

	case "add-key":
		if len(positional) != 1 {
			usage()
		}
		key := positional[0]
		if strings.HasPrefix(key, "@") {
			b, err := os.ReadFile(key[1:])
			if err != nil {
				log.Fatal(err)
			}
			key = string(b)
		}
		id, err := st.AddKey(*name, key)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("added device", id)

	case "revoke":
		if len(positional) != 1 {
			usage()
		}
		ok, err := st.Revoke(positional[0])
		if err != nil {
			log.Fatal(err)
		}
		if !ok {
			log.Fatal("no such device")
		}
		fmt.Println("revoked")

	default:
		usage()
	}
}

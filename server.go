package main

import (
	"context"
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/genuinetools/reg/clair"
	"github.com/gorilla/mux"
	wordwrap "github.com/mitchellh/go-wordwrap"
	"github.com/sirupsen/logrus"
)

const serverHelp = `Run a static UI server for a registry.`

func (cmd *serverCommand) Name() string      { return "server" }
func (cmd *serverCommand) Args() string      { return "[OPTIONS]" }
func (cmd *serverCommand) ShortHelp() string { return serverHelp }
func (cmd *serverCommand) LongHelp() string  { return serverHelp }
func (cmd *serverCommand) Hidden() bool      { return false }

func (cmd *serverCommand) Register(fs *flag.FlagSet) {
	fs.DurationVar(&cmd.interval, "interval", time.Hour, "interval to generate new index.html's at")

	fs.StringVar(&cmd.registryServer, "registry", "", "URL to the private registry (ex. r.j3ss.co)")
	fs.StringVar(&cmd.registryServer, "r", "", "URL to the private registry (ex. r.j3ss.co)")

	fs.StringVar(&cmd.clairServer, "clair", "", "url to clair instance")

	fs.StringVar(&cmd.cert, "cert", "", "path to ssl cert")
	fs.StringVar(&cmd.key, "key", "", "path to ssl key")
	fs.StringVar(&cmd.port, "port", "8080", "port for server to run on")

	fs.BoolVar(&cmd.once, "once", false, "generate an output once and then exit")
}

type serverCommand struct {
	interval       time.Duration
	registryServer string
	clairServer    string

	once bool

	cert string
	key  string
	port string
}

func (cmd *serverCommand) Run(ctx context.Context, args []string) error {
	// Create the registry client.
	r, err := createRegistryClient(cmd.registryServer)
	if err != nil {
		return err
	}

	// Create the registry controller for the handlers.
	rc := registryController{
		reg: r,
	}

	// Create a clair client if the user passed in a server address.
	if len(cmd.clairServer) < 1 {
		rc.cl, err = clair.New(cmd.clairServer, clair.Opt{
			Insecure: insecure,
			Debug:    debug,
			Timeout:  timeout,
		})
		if err != nil {
			return fmt.Errorf("creation of clair client at %s failed: %v", cmd.clairServer, err)
		}
	}

	// Get the path to the static directory.
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	staticDir := filepath.Join(wd, "static")
	templateDir := filepath.Join(staticDir, "../templates")

	// Make sure all the paths exist.
	tmplPaths := []string{
		staticDir,
		filepath.Join(templateDir, "vulns.html"),
		filepath.Join(templateDir, "repositories.html"),
		filepath.Join(templateDir, "tags.html"),
	}
	for _, path := range tmplPaths {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return fmt.Errorf("template %s not found", path)
		}
	}

	funcMap := template.FuncMap{
		"trim": func(s string) string {
			return wordwrap.WrapString(s, 80)
		},
		"color": func(s string) string {
			switch s = strings.ToLower(s); s {
			case "high":
				return "danger"
			case "critical":
				return "danger"
			case "defcon1":
				return "danger"
			case "medium":
				return "warning"
			case "low":
				return "info"
			case "negligible":
				return "info"
			case "unknown":
				return "default"
			default:
				return "default"
			}
		},
	}

	rc.tmpl = template.Must(template.New("").Funcs(funcMap).ParseGlob(templateDir + "/*.html"))

	// Create the initial index.
	logrus.Info("creating initial static index")
	if err := rc.repositories(staticDir, true); err != nil {
		return fmt.Errorf("creating index failed: %v", err)
	}

	if cmd.once {
		logrus.Info("output generated, exiting...")
		return nil
	}

	ticker := time.NewTicker(cmd.interval)
	go func() {
		// Create more indexes every X minutes based off interval.
		for range ticker.C {
			logrus.Info("creating timer based static index")
			if err := rc.repositories(staticDir, false); err != nil {
				logrus.Warnf("creating static index failed: %v", err)
			}
		}
	}()

	// Create mux server.
	mux := mux.NewRouter()
	mux.UseEncodedPath()

	// Static files handler.
	mux.HandleFunc("/repo/{repo}/tags", rc.tagsHandler)
	mux.HandleFunc("/repo/{repo}/tags/", rc.tagsHandler)
	mux.HandleFunc("/repo/{repo}/tag/{tag}", rc.vulnerabilitiesHandler)
	mux.HandleFunc("/repo/{repo}/tag/{tag}/", rc.vulnerabilitiesHandler)
	mux.HandleFunc("/repo/{repo}/tag/{tag}/vulns", rc.vulnerabilitiesHandler)
	mux.HandleFunc("/repo/{repo}/tag/{tag}/vulns/", rc.vulnerabilitiesHandler)
	mux.HandleFunc("/repo/{repo}/tag/{tag}/vulns.json", rc.vulnerabilitiesHandler)

	// Serve the static assets.
	staticHandler := http.FileServer(http.Dir(staticDir))
	mux.PathPrefix("/static/").Handler(http.StripPrefix("/static/", staticHandler))
	mux.Handle("/", staticHandler)

	// Set up the server.
	server := &http.Server{
		Addr:    ":" + cmd.port,
		Handler: mux,
	}
	logrus.Infof("Starting server on port %q", cmd.port)
	if len(cmd.cert) > 0 && len(cmd.key) > 0 {
		return server.ListenAndServeTLS(cmd.cert, cmd.key)
	}
	return server.ListenAndServe()
}

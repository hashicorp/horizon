package main

import (
	"fmt"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/hashicorp/go-hclog"
	"github.com/mitchellh/cli"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
)

var (
	sha1ver   string // sha1 revision used to build the program
	buildTime string // when the executable was built
)

func main() {
	var ver string
	if sha1ver == "" {
		ver = "unknown"
	} else {
		ver = sha1ver[:10] + "-" + buildTime
	}

	c := cli.NewCLI("hzn", ver)
	c.Args = os.Args[1:]
	c.Commands = map[string]cli.CommandFactory{
		"control": controlFactory,
		"dev": func() (cli.Command, error) {
			return &devServer{}, nil
		},
		"hub": hubFactory,
		"migrate": func() (cli.Command, error) {
			return &migrateRunner{}, nil
		},
	}

	fmt.Printf("hzn: %s\n", ver)

	exitStatus, err := c.Run()
	if err != nil {
		log.Println(err)
	}

	os.Exit(exitStatus)
}

func StartHealthz(L hclog.Logger) {
	healthzPort := os.Getenv("HEALTHZ_PORT")
	if healthzPort == "" {
		healthzPort = "17001"
	}

	L.Info("starting healthz/metrics server", "port", healthzPort)

	handlerOptions := promhttp.HandlerOpts{
		ErrorLog:           L.Named("prometheus_handler").StandardLogger(nil),
		ErrorHandling:      promhttp.ContinueOnError,
		DisableCompression: true,
	}

	promHandler := promhttp.HandlerFor(prometheus.DefaultGatherer, handlerOptions)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	http.ListenAndServe(":"+healthzPort, mux)
}
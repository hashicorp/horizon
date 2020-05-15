package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/agent"
	"github.com/hashicorp/horizon/pkg/discovery"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/mitchellh/cli"
	"github.com/spf13/pflag"
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
		"agent": func() (cli.Command, error) {
			r := &agentRunner{}
			if err := r.init(); err != nil {
				return nil, err
			}

			return r, nil
		},
		"proxy": func() (cli.Command, error) {
			r := &connectRunner{}
			if err := r.init(); err != nil {
				return nil, err
			}

			return r, nil
		},
		"test": func() (cli.Command, error) {
			r := &testRunner{}
			if err := r.init(); err != nil {
				return nil, err
			}

			return r, nil
		},
		"pipe": func() (cli.Command, error) {
			r := &pipeRunner{}
			if err := r.init(); err != nil {
				return nil, err
			}

			return r, nil
		},
	}

	exitStatus, err := c.Run()
	if err != nil {
		log.Println(err)
	}

	os.Exit(exitStatus)
}

type agentRunner struct {
	flags      *pflag.FlagSet
	fControl   *string
	fToken     *string
	fLocalAddr *string
	fLabels    *string
	fTest      *string
	fTCP       *string
}

func (a *agentRunner) init() error {
	a.flags = pflag.NewFlagSet("agent", pflag.ExitOnError)
	a.fControl = a.flags.String("control", "control.alpha.hzn.network", "address of control plane")
	a.fToken = a.flags.String("token", "", "authentication token")
	a.fLocalAddr = a.flags.String("addr", "", "address to forward http traffic to")
	a.fLabels = a.flags.String("labels", "", "labels to associate with service")
	a.fTest = a.flags.String("test", "", "run a test http server on the given port")
	a.fTCP = a.flags.String("tcp", "", "address of tcp server to advertise")

	return nil
}

func (a *agentRunner) Help() string {
	str := "horizon agent:"
	str += a.flags.FlagUsagesWrapped(4)
	return str
}

func (a *agentRunner) Synopsis() string {
	return "run the horizon agent"
}

type connectRunner struct {
	flags       *pflag.FlagSet
	fControl    *string
	fToken      *string
	fLabels     *string
	fTCPConnect *string
}

func (c *connectRunner) init() error {
	c.flags = pflag.NewFlagSet("agent", pflag.ExitOnError)
	c.fControl = c.flags.String("control", "control.alpha.hzn.network", "address of control plane")
	c.fToken = c.flags.String("token", "", "authentication token")
	c.fLabels = c.flags.String("labels", "", "labels to associate with service")
	c.fTCPConnect = c.flags.String("connect", "", "address to listen on which will be bridge to the given service")
	return nil
}

func (c *connectRunner) Help() string {
	str := "horizon connect:"
	str += c.flags.FlagUsagesWrapped(4)
	return str
}

func (c *connectRunner) Run(args []string) int {
	c.flags.Parse(args)

	level := hclog.Info
	L := hclog.New(&hclog.LoggerOptions{
		Name:  "hznagent",
		Level: level,
	})

	L.Info("discovering hubs")

	dc, err := discovery.NewClient(*c.fControl)
	if err != nil {
		log.Fatal(err)
	}

	L.Info("refreshing data")

	ctx := context.Background()

	err = dc.Refresh(ctx)
	if err != nil {
		log.Fatal(err)
	}

	L.Info("starting agent")

	g, err := agent.NewAgent(L.Named("agent"))
	if err != nil {
		log.Fatal(err)
	}

	g.Token = *c.fToken

	err = g.Start(ctx, dc)
	if err != nil {
		log.Fatal(err)
	}

	labels := pb.ParseLabelSet(*c.fLabels)
	L.Info("starting tcp listener", "addr", *c.fTCPConnect, "labels", labels)
	l, err := net.Listen("tcp", *c.fTCPConnect)
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		for {
			lc, err := l.Accept()
			if err != nil {
				L.Error("error accepting new connections", "error", err)
				os.Exit(1)
			}

			rc, err := g.Connect(labels)
			if err != nil {
				L.Error("error connecting to service", "error", err)
				lc.Close()
				continue
			}

			go func() {
				defer lc.Close()
				defer rc.Close()

				go func() {
					defer rc.Close()
					io.Copy(rc, lc)
				}()

				io.Copy(lc, rc)
				L.Info("tcp session ended")
			}()
		}
	}()

	err = g.Wait(ctx)
	if err != nil {
		log.Fatal(err)
	}

	return 0
}

func (c *connectRunner) Synopsis() string {
	return "proxy to a TCP server provided by a horizon service"
}

type testRunner struct {
	flags *pflag.FlagSet
	fAddr *string
}

func (r *testRunner) init() error {
	r.flags = pflag.NewFlagSet("agent", pflag.ExitOnError)
	r.fAddr = r.flags.String("addr", "127.0.0.1:8080", "Address to listen on for HTTP requests")
	return nil
}

func (t *testRunner) Help() string {
	return "Run a test HTTP server"
}

func (t *testRunner) Synopsis() string {
	return "Run a test HTTP server"
}

func (t *testRunner) Run(args []string) int {
	t.flags.Parse(args)

	level := hclog.Info
	L := hclog.New(&hclog.LoggerOptions{
		Name:  "hznagent",
		Level: level,
	})

	L.Info("running test http server", "port", *t.fAddr)
	err := http.ListenAndServe(":"+*t.fAddr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "this is a test server\n")
	}))
	if err != nil {
		log.Fatal(err)
	}
	return 0
}

func (a *agentRunner) Run(args []string) int {
	a.flags.Parse(args)

	level := hclog.Info
	L := hclog.New(&hclog.LoggerOptions{
		Name:  "hznagent",
		Level: level,
	})

	L.Info("discovering hubs")

	dc, err := discovery.NewClient(*a.fControl)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	L.Info("refreshing data")

	err = dc.Refresh(ctx)
	if err != nil {
		log.Fatal(err)
	}

	L.Info("starting agent")

	g, err := agent.NewAgent(L.Named("agent"))
	if err != nil {
		log.Fatal(err)
	}

	g.Token = *a.fToken

	if *a.fLocalAddr != "" {
		L.Info("registered http service", "address", *a.fLocalAddr)
		_, err = g.AddService(&agent.Service{
			Type:    "http",
			Labels:  pb.ParseLabelSet(*a.fLabels),
			Handler: agent.HTTPHandler("http://" + *a.fLocalAddr),
		})

		if err != nil {
			log.Fatal(err)
		}
	}

	if *a.fTCP != "" {
		L.Info("registered tcp service", "address", *a.fTCP)
		_, err = g.AddService(&agent.Service{
			Type:    "tcp",
			Labels:  pb.ParseLabelSet(*a.fLabels),
			Handler: agent.TCPHandler(*a.fTCP),
		})

		if err != nil {
			log.Fatal(err)
		}
	}

	err = g.Start(ctx, dc)
	if err != nil {
		log.Fatal(err)
	}

	err = g.Wait(ctx)
	if err != nil {
		log.Fatal(err)
	}

	return 0
}

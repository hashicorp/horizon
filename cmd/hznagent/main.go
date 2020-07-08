package main

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

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
			r := &proxyRunner{}
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

func Token(flag *string) string {
	token := *flag
	if token != "" {
		return token
	}

	token = os.Getenv("HORIZON_TOKEN")
	if token == "" {
		log.Fatalln("no token provided")
	}

	if token[0] == '@' {
		data, err := ioutil.ReadFile(token[1:])
		if err != nil {
			log.Fatalf("error reading file specified by HORIZON_TOKEN: %s", err)
		}

		token = strings.TrimSpace(string(data))
	}

	return token
}

type proxyRunner struct {
	flags    *pflag.FlagSet
	fControl *string
	fToken   *string
	fLabels  *string
	fListen  *string
	fVerbose *int
}

func (c *proxyRunner) init() error {
	c.flags = pflag.NewFlagSet("agent", pflag.ExitOnError)
	c.fControl = c.flags.String("control", "control.alpha.hzn.network", "address of control plane")
	c.fToken = c.flags.String("token", "", "authentication token")
	c.fLabels = c.flags.StringP("labels", "l", "", "labels to associate with service")
	c.fListen = c.flags.StringP("listen", "p", "", "address to listen on which will be bridge to the given service")
	c.fVerbose = c.flags.CountP("verbose", "v", "increase verbosity of output")
	return nil
}

func (c *proxyRunner) Help() string {
	str := "horizon connect:"
	str += c.flags.FlagUsagesWrapped(4)
	return str
}

func (c *proxyRunner) Run(args []string) int {
	c.flags.Parse(args)

	level := hclog.Info

	switch *c.fVerbose {
	case 1:
		level = hclog.Debug
	case 2:
		level = hclog.Trace
	}

	L := hclog.New(&hclog.LoggerOptions{
		Name:  "hznagent",
		Level: level,
	})

	target := *c.fListen
	if strings.IndexByte(target, ':') == -1 {
		_, err := strconv.Atoi(target)
		if err == nil {
			target = "127.0.0.1:" + target
		} else {
			fmt.Fprintf(os.Stderr, "Unable to interpret '%s' as TCP target address", target)
			return 1
		}
	}

	labels := pb.ParseLabelSet(*c.fLabels)
	L.Info("starting tcp listener", "addr", target, "labels", labels)

	L.Debug("discovering hubs")

	dc, err := discovery.NewClient(*c.fControl)
	if err != nil {
		log.Fatal(err)
	}

	L.Debug("refreshing data")

	ctx := context.Background()

	err = dc.Refresh(ctx)
	if err != nil {
		log.Fatal(err)
	}

	L.Debug("starting agent")

	g, err := agent.NewAgent(L.Named("agent"))
	if err != nil {
		log.Fatal(err)
	}

	g.Token = Token(c.fToken)

	err = g.Start(ctx, dc)
	if err != nil {
		log.Fatal(err)
	}

	l, err := net.Listen("tcp", target)
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
			}()
		}
	}()

	L.Info("agent running")
	err = g.Wait(ctx)
	if err != nil {
		log.Fatal(err)
	}

	return 0
}

func (c *proxyRunner) Synopsis() string {
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

type agentRunner struct {
	flags    *pflag.FlagSet
	fControl *string
	fToken   *string
	fHTTP    *string
	fLabels  *string
	fTCP     *string
	fVerbose *int
}

func (a *agentRunner) init() error {
	a.flags = pflag.NewFlagSet("agent", pflag.ExitOnError)
	a.fControl = a.flags.String("control", "control.alpha.hzn.network", "address of control plane")
	a.fToken = a.flags.String("token", "", "authentication token")
	a.fLabels = a.flags.StringP("labels", "l", "", "labels to associate with service")
	a.fTCP = a.flags.String("tcp", "", "address of tcp server to advertise")
	a.fHTTP = a.flags.String("http", "", "address to forward http traffic to")
	a.fVerbose = a.flags.CountP("verbose", "v", "increase verbosity of output")

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

func (a *agentRunner) Run(args []string) int {
	a.flags.Parse(args)

	level := hclog.Info

	switch *a.fVerbose {
	case 1:
		level = hclog.Debug
	case 2:
		level = hclog.Trace
	}

	L := hclog.New(&hclog.LoggerOptions{
		Name:  "hznagent",
		Level: level,
	})

	ctx := hclog.WithContext(context.Background(), L)

	L.Debug("starting agent")

	g, err := agent.NewAgent(L.Named("agent"))
	if err != nil {
		log.Fatal(err)
	}

	g.Token = Token(a.fToken)

	var setup bool

	if *a.fHTTP != "" {
		target := *a.fHTTP
		if strings.IndexByte(target, ':') == -1 {
			_, err := strconv.Atoi(target)
			if err == nil {
				target = "127.0.0.1:" + target
			} else {
				target = target + ":80"
			}
		}

		L.Info("registered http service", "address", target)
		_, err = g.AddService(&agent.Service{
			Type:    "http",
			Labels:  pb.ParseLabelSet(*a.fLabels),
			Handler: agent.HTTPHandler("http://" + target),
		})

		if err != nil {
			log.Fatal(err)
		}

		setup = true
	}

	if *a.fTCP != "" {
		target := *a.fTCP
		if strings.IndexByte(target, ':') == -1 {
			_, err := strconv.Atoi(target)
			if err == nil {
				target = "127.0.0.1:" + target
			} else {
				fmt.Fprintf(os.Stderr, "Unable to interpret '%s' as TCP target address", target)
				return 1
			}
		}

		L.Info("registered tcp service", "address", target)
		_, err = g.AddService(&agent.Service{
			Type:    "tcp",
			Labels:  pb.ParseLabelSet(*a.fLabels),
			Handler: agent.TCPHandler(target),
		})

		if err != nil {
			log.Fatal(err)
		}

		setup = true
	}

	if !setup {
		L.Error("no services defined therefore no reason to run")
		return 1
	}

	L.Debug("discovering hubs")

	dc, err := discovery.NewClient(*a.fControl)
	if err != nil {
		log.Fatal(err)
	}

	L.Debug("refreshing data")

	err = dc.Refresh(ctx)
	if err != nil {
		log.Fatal(err)
	}

	err = g.Start(ctx, dc)
	if err != nil {
		log.Fatal(err)
	}

	L.Info("agent running")

	err = g.Wait(ctx)
	if err != nil {
		log.Fatal(err)
	}

	return 0
}

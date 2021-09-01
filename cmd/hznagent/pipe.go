package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/agent"
	"github.com/hashicorp/horizon/pkg/discovery"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/spf13/pflag"
)

type pipeRunner struct {
	flags    *pflag.FlagSet
	fControl *string
	fHub     *string
	fToken   *string
	fId      *string
	fListen  *bool
	fVerbose *int
}

func (c *pipeRunner) init() error {
	c.flags = pflag.NewFlagSet("agent", pflag.ExitOnError)
	c.fControl = c.flags.String("control", "control.alpha.hzn.network", "address of control plane")
	c.fHub = c.flags.String("hub", "", "address of a hub to connect to")
	c.fToken = c.flags.String("token", "", "authentication token")
	c.fId = c.flags.String("id", "", "pipe identifier")
	c.fListen = c.flags.Bool("listen", false, "listen for a pipe connection")
	c.fVerbose = c.flags.CountP("verbose", "v", "increase verbosity of output")
	return nil
}

func (c *pipeRunner) Help() string {
	str := "horizon pipe:"
	str += c.flags.FlagUsagesWrapped(4)
	return str
}

type pipeHandler struct {
	cancel func()
}

func (p *pipeHandler) HandleRequest(ctx context.Context, L hclog.Logger, sctx agent.ServiceContext) error {
	defer p.cancel()
	defer sctx.Close()

	r := sctx.Reader()
	w := sctx.Writer()

	defer w.Close()

	go func() {
		defer w.Close()
		io.Copy(w, os.Stdin)
	}()

	io.Copy(os.Stdout, r)

	return nil
}

func (c *pipeRunner) Run(args []string) int {
	c.flags.Parse(args)

	level := hclog.Warn

	switch *c.fVerbose {
	case 1:
		level = hclog.Info
	case 2:
		level = hclog.Debug
	case 3:
		level = hclog.Trace
	}

	L := hclog.New(&hclog.LoggerOptions{
		Name:  "hznagent",
		Level: level,
	})

	ctx := context.Background()

	var config discovery.HubConfigProvider

	if c.fHub != nil {
		L.Debug("using static hub config")

		config = discovery.HubConfigs(discovery.HubConfig{
			Name:     "cli",
			Addr:     *c.fHub,
			Insecure: true,
		})
	} else {
		L.Debug("discovering hub config")

		dc, err := discovery.NewClient(*c.fControl)
		if err != nil {
			log.Fatal(err)
		}

		L.Debug("refreshing data")

		err = dc.Refresh(ctx)
		if err != nil {
			log.Fatal(err)
		}

		config = dc
	}

	L.Debug("starting agent")

	g, err := agent.NewAgent(L.Named("agent"))
	if err != nil {
		log.Fatal(err)
	}

	g.Token = Token(c.fToken)

	if *c.fId == "" {
		*c.fId = pb.NewULID().SpecString()
	}

	labels := pb.MakeLabels("type", "pipe", "pipe-id", *c.fId)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if *c.fListen {
		var ph pipeHandler
		ph.cancel = cancel

		_, err = g.AddService(&agent.Service{
			Type:    "pipe",
			Labels:  labels,
			Handler: &ph,
		})

		if err != nil {
			log.Fatal(err)
		}
	}

	err = g.Start(ctx, config)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Fprintf(os.Stderr, "Identifier: %s", *c.fId)

	L.Info("pipe identifier", "id", *c.fId, "labels", labels)

	if !*c.fListen {
		go func() {
			defer cancel()
			rc, err := g.Connect(labels)
			if err != nil {
				L.Error("error connecting to service", "error", err)
				return
			}

			defer rc.Close()

			go func() {
				defer rc.Close()
				io.Copy(rc, os.Stdin)
			}()

			io.Copy(os.Stdout, rc)
		}()
	}

	L.Debug("agent running")
	err = g.Wait(ctx)
	if err != nil {
		log.Fatal(err)
	}

	return 0
}

func (c *pipeRunner) Synopsis() string {
	return "proxy to a TCP server provided by a horizon service"
}

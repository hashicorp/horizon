package main

import (
	"context"
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
	fToken   *string
	fId      *string
	fListen  *bool
}

func (c *pipeRunner) init() error {
	c.flags = pflag.NewFlagSet("agent", pflag.ExitOnError)
	c.fControl = c.flags.String("control", "control.alpha.hzn.network", "address of control plane")
	c.fToken = c.flags.String("token", "", "authentication token")
	c.fId = c.flags.String("id", "", "pipe identifier")
	c.fListen = c.flags.Bool("listen", false, "listen for a pipe connection")
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

	level := hclog.Info
	L := hclog.New(&hclog.LoggerOptions{
		Name:   "hznagent",
		Level:  level,
		Output: os.Stderr,
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

	err = g.Start(ctx, dc)
	if err != nil {
		log.Fatal(err)
	}

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

	err = g.Wait(ctx)
	if err != nil {
		log.Fatal(err)
	}

	return 0
}

func (c *pipeRunner) Synopsis() string {
	return "proxy to a TCP server provided by a horizon service"
}

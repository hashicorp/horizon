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
	"github.com/spf13/pflag"
)

var (
	fControl    = pflag.String("control", "control.alpha.hzn.network", "address of control plane")
	fToken      = pflag.String("token", "", "authentication token")
	fLocalAddr  = pflag.String("addr", "", "address to forward http traffic to")
	fLabels     = pflag.String("labels", "", "labels to associate with service")
	fTest       = pflag.String("test", "", "run a test http server on the given port")
	fTCP        = pflag.String("tcp", "", "address of tcp server to advertise")
	fTCPConnect = pflag.String("tcp-connect", "", "address to listen on which will be bridge to the given service")
)

func main() {
	pflag.Parse()

	level := hclog.Info
	L := hclog.New(&hclog.LoggerOptions{
		Name:  "hznagent",
		Level: level,
	})

	if *fTest != "" {
		L.Info("running test http server", "port", *fTest)
		err := http.ListenAndServe(":"+*fTest, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "this is a test server\n")
		}))
		if err != nil {
			log.Fatal(err)
		}
		return
	}

	L.Info("discovering hubs")

	dc, err := discovery.NewClient(*fControl)
	if err != nil {
		log.Fatal(err)
	}

	L.Info("refreshing data")

	err = dc.Refresh()
	if err != nil {
		log.Fatal(err)
	}

	best, err := dc.Best(1)
	if err != nil {
		log.Fatal(err)
	}

	if len(best) == 0 {
		log.Fatalln("no hubs connected to control plane")
	}

	ctx := context.Background()

	L.Info("starting agent")

	g, err := agent.NewAgent(L.Named("agent"))
	if err != nil {
		log.Fatal(err)
	}

	g.Token = *fToken

	if *fLocalAddr != "" {
		L.Info("registered http service", "address", *fLocalAddr)
		_, err = g.AddService(&agent.Service{
			Type:    "http",
			Labels:  pb.ParseLabelSet(*fLabels),
			Handler: agent.HTTPHandler("http://" + *fLocalAddr),
		})

		if err != nil {
			log.Fatal(err)
		}
	}

	if *fTCP != "" {
		L.Info("registered tcp service", "address", *fTCP)
		_, err = g.AddService(&agent.Service{
			Type:    "tcp",
			Labels:  pb.ParseLabelSet(*fLabels),
			Handler: agent.TCPHandler(*fTCP),
		})

		if err != nil {
			log.Fatal(err)
		}
	}

	L.Info("connecting to hub", "address", best[0])

	err = g.Start(ctx, []agent.HubConfig{
		{
			Addr:     best[0] + ":443",
			Insecure: true,
		},
	})

	if err != nil {
		log.Fatal(err)
	}

	if *fTCPConnect != "" {
		labels := pb.ParseLabelSet(*fLabels)
		L.Info("starting tcp listener", "addr", *fTCPConnect, "labels", labels)
		l, err := net.Listen("tcp", *fTCPConnect)
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
	}

	err = g.Wait(ctx)
	if err != nil {
		log.Fatal(err)
	}
}

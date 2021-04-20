package main

import (
	"context"
	consul "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/hub"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/mitchellh/cli"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
)

func hubFactory() (cli.Command, error) {
	return &hubRunner{}, nil
}

type hubRunner struct{}

func (h *hubRunner) Help() string {
	return "Start a hub"
}

func (h *hubRunner) Synopsis() string {
	return "Start a hub"
}

func (h *hubRunner) Run(args []string) int {
	L := hclog.L().Named("hub")

	if os.Getenv("DEBUG") != "" {
		L.SetLevel(hclog.Trace)
	}

	token := os.Getenv("TOKEN")
	if token == "" {
		log.Fatal("missing TOKEN")
	}

	insecure := os.Getenv("INSECURE") == "1"
	insecureSkipVerify := os.Getenv("INSECURE_SKIP_VERIFY") == "1"

	addr := os.Getenv("CONTROL_ADDR")
	if addr == "" {
		log.Fatal("missing ADDR")
	}

	port := os.Getenv("PORT")
	if port == "" {
		L.Info("defaulting port to 443")
		port = "443"
	}

	httpPort := os.Getenv("HTTP_PORT")

	ctx := hclog.WithContext(context.Background(), L)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGQUIT)

	go func() {
		for {
			s := <-sigs
			L.Info("signal received, closing down", "signal", s)
			cancel()
		}
	}()

	sid := os.Getenv("STABLE_ID")
	if sid == "" {
		log.Fatal("missing STABLE_ID")
	}

	webNamespace := os.Getenv("WEB_NAMESPACE")
	if webNamespace == "" {
		L.Info("defaulting to namespace for frontend", "namespace", "/waypoint")
		webNamespace = "/waypoint"
	}

	id, err := pb.ParseULID(sid)
	if err != nil {
		log.Fatal(err)
	}

	tmpdir, err := ioutil.TempDir("", "hzn")
	if err != nil {
		log.Fatal(err)
	}

	defer os.RemoveAll(tmpdir)

	deployment := os.Getenv("K8_DEPLOYMENT")

	// We want to have the control client filter use ConsulHealth,
	// so we establish that here for use as a filter.

	ccfg := consul.DefaultConfigWithLogger(L)

	// Check that consul is available first.

	cc, err := consul.NewClient(ccfg)
	if err != nil {
		log.Fatal(err)
	}

	var ch *hub.ConsulHealth

	instanceId := pb.NewULID()

	var filter func(serv *pb.ServiceRoute) bool

	status := cc.Status()
	leader, err := status.Leader()
	if err == nil {
		L.Info("consul running, leader detected", "leader", leader)
		L.Info("starting consul health monitoring")

		ch, err = hub.NewConsulHealth(instanceId.SpecString(), ccfg)
		if err != nil {
			log.Fatal(err)
		}

		filter = func(serv *pb.ServiceRoute) bool {
			return ch.Available(serv.Hub.SpecString())
		}
	} else {
		L.Warn("consul not available, no consul health monitoring done")
		filter = func(serv *pb.ServiceRoute) bool {
			return true
		}
	}

	client, err := control.NewClient(ctx, control.ClientConfig{
		Id:                 id,
		InstanceId:         instanceId,
		Token:              token,
		Version:            "test",
		Addr:               addr,
		WorkDir:            tmpdir,
		K8Deployment:       deployment,
		FilterRoute:        filter,
		Insecure:           insecure,
		InsecureSkipVerify: insecureSkipVerify,
	})

	if deployment != "" {
		err = client.ConnectToKubernetes()
		if err != nil {
			L.Error("error connecting to kubernetes", "error", err)
		}

		// Best to keep running here rather than fail so that hubs
		// don't go into crash loops but rather just don't the ability to update
		// themselves.
	}

	defer func() {
		// Get a new context to process the closure because the main one
		// is most likely closed. We also update ctx and cancel in the
		// primary closure so that the signal can cancel the close if
		// sent again.
		ctx, cancel = context.WithCancel(context.Background())
		client.Close(ctx)
	}()

	var labels *pb.LabelSet

	strLabels := os.Getenv("LOCATION_LABELS")
	if strLabels != "" {
		labels = pb.ParseLabelSet(os.Getenv(strLabels))
	}

	locs, err := client.LearnLocations(labels)
	if err != nil {
		log.Fatal(err)
	}

	err = client.BootstrapConfig(ctx)
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		err := client.Run(ctx)
		if err != nil {
			L.Error("error running control client background tasks", "error", err)
		}
	}()

	L.Info("generating token to access accounts for web")
	serviceToken, err := client.RequestServiceToken(ctx, webNamespace)
	if err != nil {
		log.Fatal(err)
	}

	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatal(err)
	}

	defer ln.Close()

	hb, err := hub.NewHub(L, client, serviceToken)
	if err != nil {
		log.Fatal(err)
	}

	for _, loc := range locs {
		L.Info("learned network location", "labels", loc.Labels, "addresses", loc.Addresses)
	}

	if httpPort != "" {
		L.Info("listen on http", "port", httpPort)
		go hb.ListenHTTP(":" + httpPort)
	}

	go StartHealthz(L)

	if ch != nil {
		L.Info("starting ConsulHeath, monitoring other hubs and advertising self status")
		err = ch.Start(ctx, L)
		if err != nil {
			log.Fatal(err)
		}
	}

	err = hb.Run(ctx, ln)
	if err != nil {
		log.Fatal(err)
	}

	return 0
}

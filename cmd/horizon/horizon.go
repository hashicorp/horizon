package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/agent"
	"github.com/hashicorp/horizon/pkg/hub"
	"github.com/hashicorp/horizon/pkg/web"
)

var (
	fHello    = flag.String("hello", "", "listen on the given address with a hello world server")
	fHub      = flag.Bool("hub", false, "listen on the given address with a hub")
	fAgent    = flag.String("agent", "", "as an agent, serve traffic from the given server")
	fHubAddr  = flag.String("hub-addr", "localhost:22100", "connect to the given hub as an agent")
	fHTTPAddr = flag.String("http-addr", "localhost:22200", "address to run the http frontend on")
)

func main() {
	flag.Parse()

	if *fHello != "" {
		runHello()
		return
	}

	if *fAgent != "" {
		runAgent()
		return
	}

	if *fHub {
		runHub()
		return
	}

	fmt.Println("pass -hello, -agent, or -hub")
}

func runHello() {
	L := hclog.L().Named("hello")
	http.ListenAndServe(*fHello, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		L.Info("request", "method", req.Method, "path", req.URL.RawPath)
		defer L.Info("request ended", "method", req.Method, "path", req.URL.RawPath)

		fmt.Fprintf(w, "hello from horizon hello-world\n")
	}))
}

func runAgent() {
	L := hclog.L().Named("agent")
	L.SetLevel(hclog.Trace)

	g, err := agent.NewAgent(L, *fAgent)
	if err != nil {
		log.Fatal(err)
	}

	c, err := net.Dial("tcp", *fHubAddr)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	g.Nego(ctx, L, c)

	select {}
}

func runHub() {
	L := hclog.L().Named("hub")
	L.SetLevel(hclog.Trace)

	h, err := hub.NewHub(L)
	if err != nil {
		log.Fatal(err)
	}

	l, err := net.Listen("tcp", *fHubAddr)
	if err != nil {
		log.Fatal(err)
	}

	L.Info("hub started", "hub-addr", *fHubAddr, "http-addr", *fHTTPAddr)

	var frontend web.Frontend
	frontend.Performer = h
	frontend.L = L.Named("web")

	go http.ListenAndServe(*fHTTPAddr, &frontend)

	ctx := context.Background()

	err = h.Serve(ctx, l)
	if err != nil {
		log.Fatal(err)
	}
}

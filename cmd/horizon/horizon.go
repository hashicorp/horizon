package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/agent"
	"github.com/hashicorp/horizon/pkg/data"
	"github.com/hashicorp/horizon/pkg/hub"
	"github.com/hashicorp/horizon/pkg/noiseconn"
	"github.com/hashicorp/horizon/pkg/registry"
	"github.com/hashicorp/horizon/pkg/web"
)

var (
	fHello    = flag.String("hello", "", "listen on the given address with a hello world server")
	fHub      = flag.Bool("hub", false, "listen on the given address with a hub")
	fAgent    = flag.String("agent", "", "as an agent, serve traffic from the given server")
	fHubAddr  = flag.String("hub-addr", "localhost:22100", "connect to the given hub as an agent")
	fHTTPAddr = flag.String("http-addr", "localhost:22200", "address to run the http frontend on")
	fToken    = flag.String("token", "", "token to authenticate the agent with")
	fLabels   = flag.String("labels", "env=test", "labels to associate with this agent")
	fTLS      = flag.String("tls", "", "activate tls and store data in the given path")
	fSuffix   = flag.String("domain-suffix", ".localhost", "suffix to apply to generated domains")
	fEmail    = flag.String("email", "", "email address to use for generated certs")
	fDB       = flag.String("db", "horizon.db", "path to store hub data")
	fPeerKey  = flag.String("peer-key", "", "peer public key")
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
		user, pass, _ := req.BasicAuth()

		L.Info("request",
			"method", req.Method,
			"path", req.URL.Path,
			"query", req.URL.RawQuery,
			"fragement", req.URL.Fragment,
			"auth", user+":"+pass,
		)

		fmt.Fprintf(w, "hello from horizon hello-world\n")
	}))
}

func runAgent() {
	L := hclog.L().Named("agent")
	L.SetLevel(hclog.Trace)

	key, err := noiseconn.GenerateKey()
	if err != nil {
		log.Fatal(err)
	}

	g, err := agent.NewAgent(L, *fAgent, key)
	if err != nil {
		log.Fatal(err)
	}

	g.Token = *fToken
	for _, label := range strings.Split(*fLabels, ",") {
		g.Labels = append(g.Labels, strings.TrimSpace(label))
	}

	c, err := net.Dial("tcp", *fHubAddr)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	g.Nego(ctx, L, c, *fPeerKey)

	select {}
}

func runHub() {
	key := registry.RandomKey()

	db, err := data.NewBolt(*fDB)
	if err != nil {
		log.Fatal(err)
	}

	reg, err := registry.NewRegistry(key, *fSuffix, db)
	if err != nil {
		log.Fatal(err)
	}

	L := hclog.L().Named("hub")
	L.SetLevel(hclog.Trace)

	// To pickup logs from certmagic et al.
	log.SetOutput(L.StandardWriter(&hclog.StandardLoggerOptions{InferLevels: true}))

	if db.Empty() {
		acc, err := reg.AddAccount(L)
		if err != nil {
			log.Fatal(err)
		}

		token, err := reg.Token(L, acc)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Printf("token: %s\n", token)

		ioutil.WriteFile("test-token", []byte(token), 0755)
	}

	dkey, err := noiseconn.GenerateKey()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("peer-key: %s\n", noiseconn.PublicKey(dkey))

	ioutil.WriteFile("peer-key", []byte(noiseconn.PublicKey(dkey)), 0755)

	h, err := hub.NewHub(L, reg, dkey)
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

	if *fTLS != "" {
		tls, err := web.NewTLS(L, *fTLS, *fEmail, true, db.CertStorage(), func(name string) error {
			return reg.CertDecision(L, name)
		})
		if err != nil {
			log.Fatal(err)
		}

		go tls.ListenAndServe(":443", &frontend)
	}

	ctx := context.Background()

	err = h.Serve(ctx, l)
	if err != nil {
		log.Fatal(err)
	}
}

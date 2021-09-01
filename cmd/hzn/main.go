package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	consul "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/config"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/discovery"
	"github.com/hashicorp/horizon/pkg/grpc/lz4"
	grpctoken "github.com/hashicorp/horizon/pkg/grpc/token"
	"github.com/hashicorp/horizon/pkg/hub"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/periodic"
	"github.com/hashicorp/horizon/pkg/tlsmanage"
	"github.com/hashicorp/horizon/pkg/utils"
	"github.com/hashicorp/horizon/pkg/workq"
	"github.com/hashicorp/vault/api"
	"github.com/jinzhu/gorm"
	"github.com/mitchellh/cli"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
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
		"config-gen": func() (cli.Command, error) {
			return &configGen{}, nil
		},
	}

	fmt.Printf("hzn: %s\n", ver)

	exitStatus, err := c.Run()
	if err != nil {
		log.Println(err)
	}

	os.Exit(exitStatus)
}

func controlFactory() (cli.Command, error) {
	return &controlServer{}, nil
}

type configGen struct{}

func (m *configGen) Help() string {
	return "run any migrations"
}

func (m *configGen) Synopsis() string {
	return "run any migrations"
}

func (mr *configGen) Run(args []string) int {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}

	key := base64.StdEncoding.EncodeToString(priv)
	fmt.Printf("SIGNING_KEY=%s\n", key)

	token := make([]byte, 16)

	io.ReadFull(rand.Reader, token)
	fmt.Printf("REGISTER_TOKEN=%s\n", base64.StdEncoding.EncodeToString(token))

	io.ReadFull(rand.Reader, token)
	fmt.Printf("OPS_TOKEN=%s\n", base64.StdEncoding.EncodeToString(token))

	return 0
}

type migrateRunner struct{}

func (m *migrateRunner) Help() string {
	return "run any migrations"
}

func (m *migrateRunner) Synopsis() string {
	return "run any migrations"
}

func (mr *migrateRunner) Run(args []string) int {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("no DATABASE_URL provided")
	}

	migPath := os.Getenv("MIGRATIONS_PATH")
	if migPath == "" {
		migPath = "/migrations"
	}

	m, err := migrate.New("file://"+migPath, url)
	if err != nil {
		log.Fatal(err)
	}

	err = m.Up()
	if err != nil {
		log.Fatal(err)
	}

	return 0
}

func hubFactory() (cli.Command, error) {
	return &hubRunner{}, nil
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

type controlServer struct{}

func (c *controlServer) Help() string {
	return "Start a control server"
}

func (c *controlServer) Synopsis() string {
	return "Start a control server"
}

func (c *controlServer) Run(args []string) int {
	level := hclog.Info
	if os.Getenv("DEBUG") != "" {
		level = hclog.Trace
	}

	L := hclog.New(&hclog.LoggerOptions{
		Name:  "control",
		Level: level,
		Exclude: hclog.ExcludeFuncs{
			hclog.ExcludeByPrefix("http: TLS handshake error from").Exclude,
		}.Exclude,
	})

	L.Info("log level configured", "level", level)
	L.Trace("starting server")

	staticKey := os.Getenv("SIGNING_KEY")

	var (
		vc  *api.Client
		err error
	)

	if staticKey == "" {
		L.Info("no static signing key, using vault for key")

		vcfg := api.DefaultConfig()

		vc, err = api.NewClient(vcfg)
		if err != nil {
			log.Fatal(err)
		}

		// If we have token AND this is kubernetes, then let's try to get a token
		if vc.Token() == "" {
			f, err := os.Open("/var/run/secrets/kubernetes.io/serviceaccount/token")
			if err == nil {
				L.Info("attempting to login to vault via kubernetes auth")

				data, err := ioutil.ReadAll(f)
				if err != nil {
					log.Fatal(err)
				}

				f.Close()

				sec, err := vc.Logical().Write("auth/kubernetes/login", map[string]interface{}{
					"role": "horizon",
					"jwt":  string(bytes.TrimSpace(data)),
				})
				if err != nil {
					log.Fatal(err)
				}

				if sec == nil {
					log.Fatal("unable to login to get token")
				}

				vc.SetToken(sec.Auth.ClientToken)

				L.Info("retrieved token from vault", "accessor", sec.Auth.Accessor)

				go func() {
					tic := time.NewTicker(time.Hour)
					for {
						<-tic.C
						vc.Auth().Token().RenewSelf(86400)
					}
				}()
			}
		}
	}

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("no DATABASE_URL provided")
	}

	db, err := gorm.Open("postgres", url)
	if err != nil {
		log.Fatal(err)
	}

	var sess *session.Session

	bucket := os.Getenv("S3_BUCKET")
	if bucket != "" {
		L.Info("No s3 bucket provided, disabling S3 functionality")
		sess = session.New()
	}

	ctx := hclog.WithContext(context.Background(), L)

	var (
		key, cert []byte
		tlsmgr    *tlsmanage.Manager
	)

	domain := os.Getenv("HUB_DOMAIN")
	if domain != "" {
		L.Info("hub domain configured, using LetsEncrypt for certs")

		staging := os.Getenv("LETSENCRYPT_STAGING") != ""

		tlsmgr, err = tlsmanage.NewManager(tlsmanage.ManagerConfig{
			L:           L,
			Domain:      domain,
			VaultClient: vc,
			Staging:     staging,
		})
		if err != nil {
			log.Fatal(err)
		}

		zoneId := os.Getenv("ZONE_ID")
		if zoneId == "" {
			log.Fatal("missing ZONE_ID")
		}

		err = tlsmgr.SetupRoute53(sess, zoneId)
		if err != nil {
			log.Fatal(err)
		}

		cert, key, err = tlsmgr.HubMaterial(ctx)
		if err != nil {
			log.Fatal(err)
		}

	} else {
		L.Info("no hub domain configured, assuming crypto done outside scope")
	}

	regTok := os.Getenv("REGISTER_TOKEN")
	if regTok == "" {
		log.Fatal("missing REGISTER_TOKEN")
	}

	opsTok := os.Getenv("OPS_TOKEN")
	if opsTok == "" {
		log.Fatal("missing OPS_TOKEN")
	}

	asnDB := os.Getenv("ASN_DB_PATH")

	hubAccess := os.Getenv("HUB_ACCESS_KEY")
	hubSecret := os.Getenv("HUB_SECRET_KEY")
	hubTag := os.Getenv("HUB_IMAGE_TAG")

	port := os.Getenv("PORT")
	if port == "" {
		port = "9833"
	}

	if os.Getenv("NO_AUTO_MIGRATIONS") == "" {
		L.Info("running migrations")
		var mr migrateRunner
		mr.Run(nil)
	}

	go StartHealthz(L)

	lm, err := control.NewConsulLockManager(ctx)
	if err != nil {
		L.Info("consul not available, disable consul lock manager")
	}

	scfg := control.ServerConfig{
		Logger: L,
		DB:     db,

		RegisterToken: regTok,
		OpsToken:      opsTok,

		VaultClient: vc,
		VaultPath:   "hzn-k1",
		KeyId:       "k1",

		AwsSession: sess,
		Bucket:     bucket,

		ASNDB: asnDB,

		HubAccessKey: hubAccess,
		HubSecretKey: hubSecret,
		HubImageTag:  hubTag,
		LockManager:  lm,
	}

	if staticKey != "" {
		data, err := base64.StdEncoding.DecodeString(staticKey)
		if err != nil {
			log.Fatalf("error decoding static-key: %s", err)
		}

		scfg.SigningKey = ed25519.PrivateKey(data)
	}

	s, err := control.NewServer(scfg)
	if err != nil {
		log.Fatal(err)
	}

	// Setup cleanup activities
	lc := &control.LogCleaner{DB: config.DB()}
	workq.RegisterHandler("cleanup-activity-log", lc.CleanupActivityLog)
	workq.RegisterPeriodicJob("cleanup-activity-log", "default", "cleanup-activity-log", nil, time.Hour)

	gs := grpc.NewServer()
	pb.RegisterControlServicesServer(gs, s)
	pb.RegisterControlManagementServer(gs, s)
	pb.RegisterFlowTopReporterServer(gs, s)
	pb.RegisterEdgeServicesServer(gs, s)

	L.Info("registered grpc services", "services", []string{"control", "mgmt", "flow", "edge"})

	li, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("error listening on port: %s", err)
	}

	hs := &http.Server{
		Addr:        ":" + port,
		IdleTimeout: 2 * time.Minute,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ProtoMajor == 2 &&
				strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {

				s.L.Info("grpc request", "url", r.URL.String())
				gs.ServeHTTP(w, r)
			} else {
				s.ServeHTTP(w, r)
			}
		}),
		ErrorLog: L.StandardLogger(&hclog.StandardLoggerOptions{
			InferLevels: true,
		}),
	}

	if tlsmgr != nil {
		hubDomain := domain
		if strings.HasPrefix(hubDomain, "*.") {
			hubDomain = hubDomain[2:]
		}

		s.SetHubTLS(cert, key, hubDomain)

		// So that when they are refreshed by the background job, we eventually pick
		// them up. Hubs are also refreshing their config on an hourly basis so they'll
		// end up picking up the new TLS material that way too.
		go periodic.Run(ctx, time.Hour, func() {
			cert, key, err := tlsmgr.RefreshFromVault()
			if err != nil {
				L.Error("error refreshing hub certs from vault")
			} else {
				s.SetHubTLS(cert, key, hubDomain)
			}
		})

		tlsCert, err := tlsmgr.Certificate()
		if err != nil {
			log.Fatal(err)
		}

		var lcfg tls.Config
		lcfg.Certificates = []tls.Certificate{tlsCert}

		hs.TLSConfig = &lcfg

		tlsmgr.RegisterRenewHandler(L, workq.GlobalRegistry)
	}

	L.Info("starting background worker")

	workq.GlobalRegistry.PrintHandlers(L)

	wl := L.Named("workq")

	worker := workq.NewWorker(wl, db, []string{"default"})
	go func() {
		err := worker.Run(ctx, workq.RunConfig{
			ConnInfo: url,
		})
		if err != nil {
			if err != context.Canceled {
				wl.Debug("workq errored out in run", "error", err)
			}
		}
	}()

	if hs.TLSConfig != nil {
		L.Info("starting http server with TLS", "addr", hs.Addr)
		err = hs.ServeTLS(li, "", "")
	} else {
		h2s := &http2.Server{}
		L.Info("starting http server without TLS", "addr", hs.Addr)
		hs.Handler = h2c.NewHandler(hs.Handler, h2s)
		err = hs.Serve(li)
	}

	if err != nil {
		log.Fatal(err)
	}

	return 0
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
		L.Info("Generating stable id to use")
		sid = pb.NewULID().String()
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

	L.Info("connecting to control: %s", "control", addr)
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

	L.Info("bootstrapping config from control")
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

type devServer struct{}

func (c *devServer) Help() string {
	return "Start a dev control server"
}

func (c *devServer) Synopsis() string {
	return "Start a dev control server"
}

func (c *devServer) Run(args []string) int {
	L := hclog.New(&hclog.LoggerOptions{
		Name:  "control",
		Level: hclog.Info,
		Exclude: hclog.ExcludeFuncs{
			hclog.ExcludeByPrefix("http: TLS handshake error from").Exclude,
		}.Exclude,
	})

	if os.Getenv("DEBUG") != "" {
		L.SetLevel(hclog.Trace)
	}

	vc := utils.SetupVault()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = config.DevDBUrl
		L.Info("using default dev url for postgres", "url", url)
	}

	db, err := gorm.Open("postgres", url)
	if err != nil {
		log.Fatal(err)
	}

	sess, err := session.NewSession(aws.NewConfig().
		WithEndpoint("http://localhost:4566").
		WithRegion("us-east-1").
		WithCredentials(credentials.NewStaticCredentials("hzn", "hzn", "hzn")).
		WithS3ForcePathStyle(true),
	)

	if err != nil {
		log.Fatalf("unable to connect to S3: %v", err)
	}

	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		bucket = "hzn-dev"
		L.Info("using hzn-dev as the S3 bucket")
	}

	_, err = s3.New(sess).CreateBucket(&s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		return 0
	}

	domain := os.Getenv("HUB_DOMAIN")
	if domain == "" {
		domain = "localdomain"
		L.Info("using localdomain as hub domain")
	}

	regTok := os.Getenv("REGISTER_TOKEN")
	if regTok == "" {
		regTok = "aabbcc"
		L.Info("using default register token", "token", regTok)
	}

	opsTok := os.Getenv("OPS_TOKEN")
	if opsTok == "" {
		opsTok = regTok
		L.Info("using default ops token", "token", opsTok)
	}

	go StartHealthz(L)

	ctx := hclog.WithContext(context.Background(), L)

	s, err := control.NewServer(control.ServerConfig{
		DB: db,

		RegisterToken: regTok,
		OpsToken:      opsTok,

		VaultClient: vc,
		VaultPath:   "hzn-dev",
		KeyId:       "dev",

		AwsSession: sess,
		Bucket:     bucket,
	})
	if err != nil {
		log.Fatal(err)
	}

	hubDomain := domain
	if strings.HasPrefix(hubDomain, "*.") {
		hubDomain = hubDomain[2:]
	}

	cert, key, err := utils.SelfSignedCert()
	if err != nil {
		log.Fatal(err)
	}

	s.SetHubTLS(cert, key, hubDomain)

	gs := grpc.NewServer()
	pb.RegisterControlServicesServer(gs, s)
	pb.RegisterControlManagementServer(gs, s)
	pb.RegisterFlowTopReporterServer(gs, s)

	li, err := net.Listen("tcp", ":24401")
	if err != nil {
		log.Fatal(err)
	}

	defer li.Close()

	go gs.Serve(li)

	hs := &http.Server{
		Addr: ":24402",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ProtoMajor == 2 &&
				strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
				gs.ServeHTTP(w, r)
			} else {
				if r.URL.Path == discovery.HTTPPath {
					w.Write([]byte(`{"hubs": [{"addresses":["127.0.0.1:24403"],"labels":{"labels":[{"name":"type","value":"dev"}]}, "name":"dev.localdomain"}]}`))
				} else {
					s.ServeHTTP(w, r)
				}
			}
		}),
		ErrorLog: L.StandardLogger(&hclog.StandardLoggerOptions{
			InferLevels: true,
		}),
	}

	L.Info("starting background worker")

	workq.GlobalRegistry.PrintHandlers(L)

	worker := workq.NewWorker(L, db, []string{"default"})
	go worker.Run(ctx, workq.RunConfig{
		ConnInfo: url,
	})

	md := make(metadata.MD)
	md.Set("authorization", regTok)

	ictx := metadata.NewIncomingContext(ctx, md)

	ctr, err := s.IssueHubToken(ictx, &pb.Noop{})
	if err != nil {
		log.Fatal(err)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGQUIT)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		for {
			s := <-sigs
			L.Info("signal received, closing down", "signal", s)
			cancel()
			hs.Close()
		}
	}()

	mgmtToken, err := s.GetManagementToken(ctx, "/waypoint")
	if err != nil {
		log.Fatal(err)
	}

	md2 := make(metadata.MD)
	md2.Set("authorization", mgmtToken)

	accountId := pb.NewULID()

	agentToken, err := s.CreateToken(
		metadata.NewIncomingContext(ctx, md2),
		&pb.CreateTokenRequest{
			Account: &pb.Account{
				AccountId: accountId,
				Namespace: "/waypoint",
			},
			Capabilities: []pb.TokenCapability{
				{
					Capability: pb.SERVE,
				},
			},
		})
	if err != nil {
		log.Fatal(err)
	}

	L.Info("dev agent token", "token", agentToken.Token)

	ioutil.WriteFile("dev-mgmt-token.txt", []byte(mgmtToken), 0644)
	ioutil.WriteFile("dev-agent-id.txt", []byte(accountId.String()), 0644)
	ioutil.WriteFile("dev-agent-token.txt", []byte(agentToken.Token), 0644)

	go c.RunHub(ctx, ctr.Token, "localhost:24401", sess, bucket)
	err = hs.ListenAndServe()
	if err != nil {
		log.Fatal(err)
	}

	return 0
}

const devHub = "01ECNJBS294ESNMG913SVX893F"

func (h *devServer) RunHub(ctx context.Context, token, addr string, sess *session.Session, bucket string) int {
	L := hclog.L().Named("hub")

	if os.Getenv("DEBUG") != "" {
		L.SetLevel(hclog.Trace)
	}

	port := "24403"
	httpPort := "24404"

	ctx = hclog.WithContext(ctx, L)

	sid := devHub

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

	gcc, err := grpc.Dial(addr,
		grpc.WithInsecure(),
		grpc.WithPerRPCCredentials(grpctoken.Token(token)),
		grpc.WithDefaultCallOptions(grpc.UseCompressor(lz4.Name)),
	)
	if err != nil {
		log.Fatal(err)
	}

	defer gcc.Close()

	gClient := pb.NewControlServicesClient(gcc)

	client, err := control.NewClient(ctx, control.ClientConfig{
		Id:                 id,
		Token:              token,
		Version:            "test",
		Client:             gClient,
		WorkDir:            tmpdir,
		Session:            sess,
		S3Bucket:           bucket,
		Insecure:           true,
		InsecureSkipVerify: true,
	})

	if err != nil {
		log.Fatal(err)
	}

	defer client.Close(ctx)

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

	L.Info("listen on http", "port", httpPort)
	go func() {
		err := hb.ListenHTTP(":" + httpPort)
		if err != nil {
			log.Fatal("unable to listen on :" + httpPort)
		}
	}()

	go StartHealthz(L)

	err = hb.Run(ctx, ln)
	if err != nil {
		log.Fatal(err)
	}

	return 0
}

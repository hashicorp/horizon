package main

import (
	"context"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/config"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/discovery"
	"github.com/hashicorp/horizon/pkg/grpc/lz4"
	grpctoken "github.com/hashicorp/horizon/pkg/grpc/token"
	"github.com/hashicorp/horizon/pkg/hub"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/utils"
	"github.com/hashicorp/horizon/pkg/workq"
	"github.com/jinzhu/gorm"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

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


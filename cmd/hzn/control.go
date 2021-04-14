package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/config"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/periodic"
	"github.com/hashicorp/horizon/pkg/tlsmanage"
	"github.com/hashicorp/horizon/pkg/workq"
	"github.com/hashicorp/vault/api"
	"github.com/jinzhu/gorm"
	"github.com/mitchellh/cli"
	"google.golang.org/grpc"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type controlServer struct{}
func controlFactory() (cli.Command, error) {
	return &controlServer{}, nil
}

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

	vcfg := api.DefaultConfig()

	vc, err := api.NewClient(vcfg)
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

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("no DATABASE_URL provided")
	}

	db, err := gorm.Open("postgres", url)
	if err != nil {
		log.Fatal(err)
	}

	sess := session.New()

	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		log.Fatal("S3_BUCKET not set")
	}

	domain := os.Getenv("HUB_DOMAIN")
	if domain == "" {
		log.Fatal("missing HUB_DOMAIN")
	}

	staging := os.Getenv("LETSENCRYPT_STAGING") != ""

	tlsmgr, err := tlsmanage.NewManager(tlsmanage.ManagerConfig{
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

	go StartHealthz(L)

	ctx := hclog.WithContext(context.Background(), L)

	cert, key, err := tlsmgr.HubMaterial(ctx)
	if err != nil {
		log.Fatal(err)
	}

	lm, err := control.NewConsulLockManager(ctx)
	if err != nil {
		log.Fatal(err)
	}

	s, err := control.NewServer(control.ServerConfig{
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
	})
	if err != nil {
		log.Fatal(err)
	}

	// Setup cleanup activities
	lc := &control.LogCleaner{DB: config.DB()}
	workq.RegisterHandler("cleanup-activity-log", lc.CleanupActivityLog)
	workq.RegisterPeriodicJob("cleanup-activity-log", "default", "cleanup-activity-log", nil, time.Hour)

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

	gs := grpc.NewServer()
	pb.RegisterControlServicesServer(gs, s)
	pb.RegisterControlManagementServer(gs, s)
	pb.RegisterFlowTopReporterServer(gs, s)

	tlsCert, err := tlsmgr.Certificate()
	if err != nil {
		log.Fatal(err)
	}

	var lcfg tls.Config
	lcfg.Certificates = []tls.Certificate{tlsCert}

	hs := &http.Server{
		TLSConfig:   &lcfg,
		Addr:        ":" + port,
		IdleTimeout: 2 * time.Minute,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ProtoMajor == 2 &&
				strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
				gs.ServeHTTP(w, r)
			} else {
				s.ServeHTTP(w, r)
			}
		}),
		ErrorLog: L.StandardLogger(&hclog.StandardLoggerOptions{
			InferLevels: true,
		}),
	}

	tlsmgr.RegisterRenewHandler(L, workq.GlobalRegistry)

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

	err = hs.ListenAndServeTLS("", "")
	if err != nil {
		log.Fatal(err)
	}

	return 0
}
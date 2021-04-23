package control_test

import (
	context "context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	consul "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/internal/testsql"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/grpc/lz4"
	grpctoken "github.com/hashicorp/horizon/pkg/grpc/token"
	"github.com/hashicorp/horizon/pkg/hub"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/testutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestE2E(t *testing.T) {
	vc := testutils.SetupVault()
	sess := testutils.AWSSession(t)

	bucket := "hzntest-" + pb.NewULID().SpecString()
	s3.New(sess).CreateBucket(&s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})

	defer testutils.DeleteBucket(s3.New(sess), bucket)

	hubcert, hubkey, err := testutils.SelfSignedCert()
	require.NoError(t, err)

	scfg := control.ServerConfig{
		VaultClient:       vc,
		VaultPath:         pb.NewULID().SpecString(),
		KeyId:             "k1",
		RegisterToken:     "aabbcc",
		AwsSession:        sess,
		Bucket:            bucket,
		DisablePrometheus: true,
		HubCert:           hubcert,
		HubKey:            hubkey,
	}

	t.Run("broadcasts to hubs registered in consul", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "hzn")
		defer db.Close()

		top, cancel := context.WithCancel(context.Background())
		defer cancel()

		cfg := scfg
		cfg.DB = db
		cfg.ConsulConfig = consul.DefaultConfig()

		s, err := control.NewServer(cfg)
		require.NoError(t, err)

		// ==== Setup server GRPC
		ctoken, err := s.LocalIssueHubToken()
		require.NoError(t, err)

		gs := grpc.NewServer()
		pb.RegisterControlServicesServer(gs, s)

		li, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		defer li.Close()

		go gs.Serve(li)

		gcc, err := grpc.Dial(li.Addr().String(),
			grpc.WithInsecure(),
			grpc.WithPerRPCCredentials(grpctoken.Token(ctoken)),
			grpc.WithDefaultCallOptions(grpc.UseCompressor(lz4.Name)),
		)

		require.NoError(t, err)

		defer gcc.Close()

		gClient := pb.NewControlServicesClient(gcc)

		id := pb.NewULID()

		client, err := control.NewClient(top, control.ClientConfig{
			Id:      id,
			Token:   ctoken,
			Version: "test",
			Client:  gClient,
			Session: sess,
		})

		require.NoError(t, err)

		err = client.BootstrapConfig(top)
		require.NoError(t, err)

		// ===== END setup server GRPC

		// SETUP HUB GRPC

		cert, err := tls.X509KeyPair(hubcert, hubkey)
		require.NoError(t, err)

		var tlscfg tls.Config
		tlscfg.Certificates = []tls.Certificate{cert}

		hl, err := tls.Listen("tcp", "127.0.0.1:0", &tlscfg)
		require.NoError(t, err)

		defer hl.Close()

		hubHealth, err := hub.NewConsulHealth("h1", cfg.ConsulConfig, hl.Addr().String())
		require.NoError(t, err)

		defer hubHealth.DeregisterService(context.Background())

		err = hubHealth.Start(top, hclog.L())
		require.NoError(t, err)

		grpcServer := grpc.NewServer()

		pb.RegisterHubServicesServer(grpcServer, &hub.InboundServer{Client: client})

		go grpcServer.Serve(hl)

		// END HUB SETUP

		md := make(metadata.MD)
		md.Set("authorization", "aabbcc")

		ctx := metadata.NewIncomingContext(top, md)

		ct, err := s.Register(ctx, &pb.ControlRegister{
			Namespace: "/",
		})

		require.NoError(t, err)

		md2 := make(metadata.MD)
		md2.Set("authorization", ct.Token)

		accountId := pb.NewULID()
		account := &pb.Account{
			Namespace: "/",
			AccountId: accountId,
		}

		md3 := make(metadata.MD)
		md3.Set("authorization", ctoken)

		labels := pb.ParseLabelSet("service=www,env=prod")

		hubId := pb.NewULID()
		serviceId := pb.NewULID()

		_, err = client.TrackAccount(account)
		require.NoError(t, err)

		time.Sleep(time.Second)

		_, err = s.AddService(
			metadata.NewIncomingContext(top, md3),
			&pb.ServiceRequest{
				Account: account,
				Hub:     hubId,
				Id:      serviceId,
				Type:    "test",
				Labels:  labels,
				Metadata: []*pb.KVPair{
					{
						Key:   "version",
						Value: "0.1x",
					},
				},
			},
		)
		require.NoError(t, err)

		time.Sleep(time.Second)

		recent, err := client.FindRecentServices(account)
		require.NoError(t, err)

		require.Equal(t, 1, len(recent))

		assert.Equal(t, serviceId, recent[0].Id)
	})
}

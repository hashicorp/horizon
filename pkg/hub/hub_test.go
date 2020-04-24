package hub

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-txdb"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/testutils"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/vault/api"
	"github.com/jinzhu/gorm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func init() {
	db := os.Getenv("DATABASE_URL")
	if db != "" {
		txdb.Register("pgtest", "postgres", db)
		dialect, _ := gorm.GetDialect("postgres")
		gorm.RegisterDialect("pgtest", dialect)
	}
}

func TestHub(t *testing.T) {
	bindPort := 24001

	vt := os.Getenv("VAULT_TOKEN")
	if vt == "" {
		t.Skip("no vault token available to test against vault")
	}

	var cfg api.Config
	cfg.Address = "http://127.0.0.1:8200"
	vc, err := api.NewClient(&cfg)
	require.NoError(t, err)

	db := os.Getenv("DATABASE_URL")
	if db == "" {
		t.Skip("missing database url, skipping postgres tests")
	}
	sess := session.New(aws.NewConfig().
		WithEndpoint("http://localhost:4566").
		WithRegion("us-east-1").
		WithS3ForcePathStyle(true),
	)

	bucket := "hzntest-" + pb.NewULID().SpecString()
	s3.New(sess).CreateBucket(&s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})

	defer testutils.DeleteBucket(s3.New(sess), bucket)

	t.Run("can create and remove a service", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		s, err := control.NewServer(control.ServerConfig{
			DB:            db,
			VaultClient:   vc,
			VaultPath:     pb.NewULID().SpecString(),
			KeyId:         "k1",
			RegisterToken: "aabbcc",
			AwsSession:    sess,
			Bucket:        bucket,
			LockTable:     "hzntest",
		})
		require.NoError(t, err)

		cert, key, err := testutils.SelfSignedCert()
		require.NoError(t, err)

		s.SetHubTLS(cert, key)

		top := context.Background()

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

		ctr, err := s.IssueHubToken(ctx, &pb.Noop{})
		require.NoError(t, err)

		agentToken, err := s.CreateToken(
			metadata.NewIncomingContext(ctx, md2),
			&pb.CreateTokenRequest{
				Account: &pb.Account{
					AccountId: accountId,
					Namespace: "/",
				},
				Capabilities: []pb.TokenCapability{
					{
						Capability: pb.SERVE,
					},
				},
			})
		require.NoError(t, err)

		gs := grpc.NewServer()
		pb.RegisterControlServicesServer(gs, s)

		li, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		defer li.Close()

		go gs.Serve(li)

		gcc, err := grpc.Dial(li.Addr().String(),
			grpc.WithInsecure(),
			grpc.WithPerRPCCredentials(control.Token(ctr.Token)))

		require.NoError(t, err)

		defer gcc.Close()

		gClient := pb.NewControlServicesClient(gcc)

		id := pb.NewULID()

		client, err := control.NewClient(ctx, control.ClientConfig{
			Id:      id,
			Token:   ctr.Token,
			Version: "test",
			Client:  gClient,
			Session: sess,
			TLSPort: bindPort,
		})

		ingressPort := bindPort

		bindPort++

		require.NoError(t, err)

		defer client.Close()

		err = client.BootstrapConfig(ctx)
		require.NoError(t, err)

		L := hclog.L()
		hub, err := NewHub(L, client)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go func() {
			err = hub.Run(ctx)
			require.NoError(t, err)
		}()

		time.Sleep(time.Second)

		var clientTlsConfig tls.Config
		clientTlsConfig.InsecureSkipVerify = true
		clientTlsConfig.NextProtos = []string{"hzn"}

		cconn, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", ingressPort), &clientTlsConfig)
		require.NoError(t, err)

		serviceId := pb.NewULID()
		labels := pb.ParseLabelSet("service=www,env=prod")

		var preamble pb.Preamble
		preamble.Token = agentToken.Token
		preamble.Services = []*pb.ServiceInfo{
			{
				ServiceId: serviceId,
				Type:      "test",
				Labels:    labels,
				Metadata: []*pb.KVPair{
					{
						Key:   "version",
						Value: "0.1x",
					},
				},
			},
		}

		fw, err := wire.NewFramingWriter(cconn)
		require.NoError(t, err)

		_, err = fw.WriteMarshal(1, &preamble)
		require.NoError(t, err)

		start := time.Now()

		fr, err := wire.NewFramingReader(cconn)
		require.NoError(t, err)

		var confirmation pb.Confirmation

		tag, _, err := fr.ReadMarshal(&confirmation)
		require.NoError(t, err)

		assert.Equal(t, uint8(1), tag)

		t.Logf("skew: %s", confirmation.Time.Time().Sub(start))

		assert.Equal(t, "connected", confirmation.Status)

		var so control.Service
		err = dbx.Check(db.First(&so))

		assert.NoError(t, err)

		assert.Equal(t, serviceId.Bytes(), so.ServiceId)
	})

	t.Run("rejects hub tokens checks the serve capability", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		s, err := control.NewServer(control.ServerConfig{
			DB:            db,
			VaultClient:   vc,
			VaultPath:     pb.NewULID().SpecString(),
			KeyId:         "k1",
			RegisterToken: "aabbcc",
			AwsSession:    sess,
			Bucket:        bucket,
			LockTable:     "hzntest",
		})
		require.NoError(t, err)

		cert, key, err := testutils.SelfSignedCert()
		require.NoError(t, err)

		s.SetHubTLS(cert, key)

		top := context.Background()

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

		ctr, err := s.IssueHubToken(ctx, &pb.Noop{})
		require.NoError(t, err)

		agentToken, err := s.CreateToken(
			metadata.NewIncomingContext(ctx, md2),
			&pb.CreateTokenRequest{
				Account: &pb.Account{
					AccountId: accountId,
					Namespace: "/",
				},
			})
		require.NoError(t, err)

		gs := grpc.NewServer()
		pb.RegisterControlServicesServer(gs, s)

		li, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		defer li.Close()

		go gs.Serve(li)

		gcc, err := grpc.Dial(li.Addr().String(),
			grpc.WithInsecure(),
			grpc.WithPerRPCCredentials(control.Token(ctr.Token)))

		require.NoError(t, err)

		defer gcc.Close()

		gClient := pb.NewControlServicesClient(gcc)

		id := pb.NewULID()

		client, err := control.NewClient(ctx, control.ClientConfig{
			Id:      id,
			Token:   ctr.Token,
			Version: "test",
			Client:  gClient,
			Session: sess,
			TLSPort: bindPort,
		})

		ingressPort := bindPort

		bindPort++

		require.NoError(t, err)

		defer client.Close()

		err = client.BootstrapConfig(ctx)
		require.NoError(t, err)

		L := hclog.L()
		hub, err := NewHub(L, client)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go func() {
			err = hub.Run(ctx)
			require.NoError(t, err)
		}()

		time.Sleep(time.Second)

		var clientTlsConfig tls.Config
		clientTlsConfig.InsecureSkipVerify = true
		clientTlsConfig.NextProtos = []string{"hzn"}

		cconn, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", ingressPort), &clientTlsConfig)
		require.NoError(t, err)

		serviceId := pb.NewULID()
		labels := pb.ParseLabelSet("service=www,env=prod")

		var preamble pb.Preamble
		preamble.Token = agentToken.Token
		preamble.Services = []*pb.ServiceInfo{
			{
				ServiceId: serviceId,
				Type:      "test",
				Labels:    labels,
				Metadata: []*pb.KVPair{
					{
						Key:   "version",
						Value: "0.1x",
					},
				},
			},
		}

		fw, err := wire.NewFramingWriter(cconn)
		require.NoError(t, err)

		_, err = fw.WriteMarshal(1, &preamble)
		require.NoError(t, err)

		start := time.Now()

		fr, err := wire.NewFramingReader(cconn)
		require.NoError(t, err)

		var confirmation pb.Confirmation

		tag, _, err := fr.ReadMarshal(&confirmation)
		require.NoError(t, err)

		assert.Equal(t, uint8(1), tag)

		assert.Equal(t, "bad-token-capability", confirmation.Status)

		t.Logf("skew: %s", confirmation.Time.Time().Sub(start))

		var so control.Service
		err = dbx.Check(db.First(&so))

		assert.Error(t, err)
	})

}

package central

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/testutils"
	"github.com/hashicorp/vault/api"
	"github.com/jinzhu/gorm"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type DevSetup struct {
	DB             *gorm.DB
	ControlClient  *control.Client
	ControlServer  *control.Server
	AgentToken     string
	HubToken       string
	HubAddr        string
	AccountId      *pb.ULID
	MgmtCtx        context.Context
	ClientListener net.Listener
}

func Dev(t *testing.T, f func(setup *DevSetup)) {
	testutils.SetupDB()

	vt := os.Getenv("VAULT_TOKEN")
	if vt == "" {
		t.Skip("no vault token available to test against vault")
	}

	var cfg api.Config
	cfg.Address = "http://127.0.0.1:8200"
	vc, err := api.NewClient(&cfg)
	require.NoError(t, err)

	dbUrl := os.Getenv("DATABASE_URL")
	if dbUrl == "" {
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

	top, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

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

	tmpdir, err := ioutil.TempDir("", "hzn")
	require.NoError(t, err)

	defer os.RemoveAll(tmpdir)

	client, err := control.NewClient(ctx, control.ClientConfig{
		Id:       id,
		Token:    ctr.Token,
		Version:  "test",
		Client:   gClient,
		Session:  sess,
		S3Bucket: bucket,
		WorkDir:  tmpdir,
	})

	require.NoError(t, err)

	defer client.Close()

	err = client.BootstrapConfig(ctx)
	require.NoError(t, err)

	ln, err := net.Listen("tcp", ":0")
	require.NoError(t, err)

	defer ln.Close()

	f(&DevSetup{
		DB:             db,
		ControlClient:  client,
		ControlServer:  s,
		HubToken:       ctr.Token,
		HubAddr:        fmt.Sprintf("127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port),
		AgentToken:     agentToken.Token,
		AccountId:      accountId,
		MgmtCtx:        metadata.NewIncomingContext(top, md2),
		ClientListener: ln,
	})
}

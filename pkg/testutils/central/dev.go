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
	"github.com/hashicorp/horizon/pkg/grpc/lz4"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/testutils"
	"github.com/jinzhu/gorm"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type DevSetup struct {
	Top            context.Context
	DB             *gorm.DB
	ControlClient  *control.Client
	ControlServer  *control.Server
	ServerAddr     string
	AgentToken     string
	HubToken       string
	HubAddr        string
	Account        *pb.Account
	MgmtCtx        context.Context
	ClientListener net.Listener
	AwsSession     *session.Session
	S3Bucket       string
	HubServToken   string
}

func Dev(t *testing.T, f func(setup *DevSetup)) {
	testutils.SetupDB()

	vc := testutils.SetupVault()

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

	defer db.Exec("TRUNCATE management_clients CASCADE")
	defer db.Exec("TRUNCATE accounts CASCADE")

	s, err := control.NewServer(control.ServerConfig{
		DB:                db,
		VaultClient:       vc,
		VaultPath:         pb.NewULID().SpecString(),
		KeyId:             "k1",
		RegisterToken:     "aabbcc",
		AwsSession:        sess,
		Bucket:            bucket,
		LockTable:         "hzntest",
		DisablePrometheus: true,
	})
	require.NoError(t, err)

	cert, key, err := testutils.SelfSignedCert()
	require.NoError(t, err)

	s.SetHubTLS(cert, key, "testdomain")

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

	hubServToken, err := s.CreateToken(
		metadata.NewIncomingContext(ctx, md2),
		&pb.CreateTokenRequest{
			Account: &pb.Account{
				AccountId: accountId,
				Namespace: "/",
			},
			Capabilities: []pb.TokenCapability{
				{
					Capability: pb.ACCESS,
					Value:      "/",
				},
				{
					Capability: pb.CONNECT,
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
		grpc.WithPerRPCCredentials(control.Token(ctr.Token)),
		grpc.WithDefaultCallOptions(grpc.UseCompressor(lz4.Name)),
	)

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

	defer client.Close(ctx)

	err = client.BootstrapConfig(ctx)
	require.NoError(t, err)

	ln, err := net.Listen("tcp", ":0")
	require.NoError(t, err)

	defer ln.Close()

	f(&DevSetup{
		Top:           top,
		DB:            db,
		ControlClient: client,
		ControlServer: s,
		ServerAddr:    li.Addr().String(),
		HubToken:      ctr.Token,
		HubAddr:       fmt.Sprintf("127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port),
		AgentToken:    agentToken.Token,
		Account: &pb.Account{
			AccountId: accountId,
			Namespace: "/",
		},
		MgmtCtx:        metadata.NewIncomingContext(top, md2),
		ClientListener: ln,
		AwsSession:     sess,
		S3Bucket:       bucket,
		HubServToken:   hubServToken.Token,
	})
}

func (s *DevSetup) NewControlClient(t *testing.T, f func(c *control.Client, li net.Listener)) {
	gcc, err := grpc.Dial(s.ServerAddr,
		grpc.WithInsecure(),
		grpc.WithPerRPCCredentials(control.Token(s.HubToken)),
		grpc.WithDefaultCallOptions(grpc.UseCompressor(lz4.Name)),
	)

	require.NoError(t, err)

	defer gcc.Close()

	gClient := pb.NewControlServicesClient(gcc)

	id := pb.NewULID()

	tmpdir, err := ioutil.TempDir("", "hzn")
	require.NoError(t, err)

	defer os.RemoveAll(tmpdir)

	client, err := control.NewClient(s.Top, control.ClientConfig{
		Id:       id,
		Token:    s.HubToken,
		Version:  "test",
		Client:   gClient,
		Session:  s.AwsSession,
		S3Bucket: s.S3Bucket,
		WorkDir:  tmpdir,
	})

	require.NoError(t, err)

	defer client.Close(s.Top)

	err = client.BootstrapConfig(s.Top)
	require.NoError(t, err)

	ln, err := net.Listen("tcp", ":0")
	require.NoError(t, err)

	defer ln.Close()

	f(client, ln)
}

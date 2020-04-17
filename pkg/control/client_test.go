package control

import (
	context "context"
	"io/ioutil"
	"net"
	"os"
	"testing"
	"time"

	"cirello.io/dynamolock"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/token"
	"github.com/hashicorp/serf/serf"
	"github.com/hashicorp/vault/api"
	"github.com/jinzhu/gorm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestClient(t *testing.T) {
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

	serfcfg := serf.DefaultConfig()
	serfcfg.NodeName = pb.NewULID().SpecString()
	serfcfg.MemberlistConfig.BindPort = 25000
	serfObj, err := serf.Create(serfcfg)
	require.NoError(t, err)

	defer serfObj.Shutdown()

	t.Run("can create and remove a service", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		var s Server
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"
		s.awsSess = sess
		s.bucket = bucket
		s.s = serfObj
		s.lockTable = "hzntest"

		s.lockMgr, err = dynamolock.New(dynamodb.New(sess), s.lockTable)
		require.NoError(t, err)

		_, err = s.lockMgr.CreateTable(s.lockTable)
		require.NoError(t, err)

		pub, err := token.SetupVault(vc, s.vaultPath)
		require.NoError(t, err)

		s.pubKey = pub

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

		gs := grpc.NewServer()
		pb.RegisterControlServicesServer(gs, &s)

		li, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		defer li.Close()

		go gs.Serve(li)

		gcc, err := grpc.Dial(li.Addr().String(),
			grpc.WithInsecure(),
			grpc.WithPerRPCCredentials(Token(ctr.Token)))

		require.NoError(t, err)

		defer gcc.Close()

		gClient := pb.NewControlServicesClient(gcc)

		id := pb.NewULID()

		client, err := NewClient(ClientConfig{
			Id:       id,
			Token:    ctr.Token,
			Version:  "test",
			Client:   gClient,
			BindPort: bindPort,
			Session:  sess,
		})

		bindPort++

		require.NoError(t, err)

		serviceId := pb.NewULID()
		labels := pb.ParseLabelSet("service=www,env=prod")

		servReq := &pb.ServiceRequest{
			Account: &pb.Account{
				Namespace: "/",
				AccountId: accountId,
			},
			Id:        serviceId,
			Type:      "test",
			LabelSets: []*pb.LabelSet{labels},
			Metadata: []*pb.KVPair{
				{
					Key:   "version",
					Value: "0.1x",
				},
			},
		}

		err = client.AddService(ctx, servReq)
		require.NoError(t, err)

		var so Service
		err = dbx.Check(db.First(&so))
		require.NoError(t, err)

		assert.Equal(t, serviceId.Bytes(), so.ServiceId)

		local, ok := client.localServices[labels.SpecString()]
		require.True(t, ok)

		require.Equal(t, 1, len(local))

		ls := local[serviceId.SpecString()]

		assert.Equal(t, serviceId, ls.Id)

		err = client.RemoveService(ctx, servReq)
		require.NoError(t, err)

		require.Equal(t, 0, len(local))

		err = dbx.Check(db.First(&so))
		assert.Error(t, err)
	})

	t.Run("lookup a service locally and remotely", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		var s Server
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"
		s.awsSess = sess
		s.bucket = bucket
		s.s = serfObj
		s.lockTable = "hzntest"

		s.lockMgr, err = dynamolock.New(dynamodb.New(sess), s.lockTable)
		require.NoError(t, err)

		_, err = s.lockMgr.CreateTable(s.lockTable)
		require.NoError(t, err)

		pub, err := token.SetupVault(vc, s.vaultPath)
		require.NoError(t, err)

		s.pubKey = pub

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

		gs := grpc.NewServer()
		pb.RegisterControlServicesServer(gs, &s)

		li, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		defer li.Close()

		go gs.Serve(li)

		gcc, err := grpc.Dial(li.Addr().String(),
			grpc.WithInsecure(),
			grpc.WithPerRPCCredentials(Token(ctr.Token)))

		require.NoError(t, err)

		defer gcc.Close()

		gClient := pb.NewControlServicesClient(gcc)

		id := pb.NewULID()

		dir, err := ioutil.TempDir("", "hzn")
		require.NoError(t, err)

		defer os.RemoveAll(dir)

		client, err := NewClient(ClientConfig{
			Id:       id,
			Token:    ctr.Token,
			Version:  "test",
			Client:   gClient,
			BindPort: bindPort,
			WorkDir:  dir,
			Session:  sess,
			S3Bucket: bucket,
		})

		bindPort++

		require.NoError(t, err)

		serviceId := pb.NewULID()
		labels := pb.ParseLabelSet("service=www,env=prod")

		servReq := &pb.ServiceRequest{
			Account: &pb.Account{
				Namespace: "/",
				AccountId: accountId,
			},
			Id:        serviceId,
			Type:      "test",
			LabelSets: []*pb.LabelSet{labels},
			Metadata: []*pb.KVPair{
				{
					Key:   "version",
					Value: "0.1x",
				},
			},
		}

		err = client.AddService(ctx, servReq)
		require.NoError(t, err)

		var so Service
		err = dbx.Check(db.First(&so))
		require.NoError(t, err)

		assert.Equal(t, serviceId.Bytes(), so.ServiceId)

		local, ok := client.localServices[labels.SpecString()]
		require.True(t, ok)

		require.Equal(t, 1, len(local))

		ls := local[serviceId.SpecString()]

		assert.Equal(t, serviceId, ls.Id)

		services, err := client.LookupService(ctx, accountId, labels)
		require.NoError(t, err)

		assert.Equal(t, 1, len(services))

		assert.Equal(t, serviceId, services[0].Id)

		hubId2 := pb.NewULID()
		serviceId2 := pb.NewULID()

		// Nuke to force an inline refresh
		delete(client.accountServices, accountId.SpecString())

		hubtoken, err := s.IssueHubToken(ctx, &pb.Noop{})
		require.NoError(t, err)

		md3 := make(metadata.MD)
		md3.Set("authorization", hubtoken.Token)

		_, err = s.AddService(
			metadata.NewIncomingContext(top, md3),
			&pb.ServiceRequest{
				Account: &pb.Account{
					Namespace: "/",
					AccountId: accountId,
				},
				Hub:       hubId2,
				Id:        serviceId2,
				Type:      "test",
				LabelSets: []*pb.LabelSet{labels},
				Metadata: []*pb.KVPair{
					{
						Key:   "version",
						Value: "0.1x",
					},
				},
			},
		)

		require.NoError(t, err)

		services, err = client.LookupService(ctx, accountId, labels)
		require.NoError(t, err)

		require.Equal(t, 2, len(services))

		assert.Equal(t, serviceId, services[0].Id)
		assert.Equal(t, serviceId2, services[1].Id)
	})

	t.Run("refreshes an account on response to a serf event", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		var s Server
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"
		s.awsSess = sess
		s.bucket = bucket
		s.s = serfObj
		s.lockTable = "hzntest"

		s.lockMgr, err = dynamolock.New(dynamodb.New(sess), s.lockTable)
		require.NoError(t, err)

		_, err = s.lockMgr.CreateTable(s.lockTable)
		require.NoError(t, err)

		pub, err := token.SetupVault(vc, s.vaultPath)
		require.NoError(t, err)

		s.pubKey = pub

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

		gs := grpc.NewServer()
		pb.RegisterControlServicesServer(gs, &s)

		li, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		defer li.Close()

		go gs.Serve(li)

		gcc, err := grpc.Dial(li.Addr().String(),
			grpc.WithInsecure(),
			grpc.WithPerRPCCredentials(Token(ctr.Token)))

		require.NoError(t, err)

		defer gcc.Close()

		gClient := pb.NewControlServicesClient(gcc)

		id := pb.NewULID()

		dir, err := ioutil.TempDir("", "hzn")
		require.NoError(t, err)

		defer os.RemoveAll(dir)

		client, err := NewClient(ClientConfig{
			Id:       id,
			Token:    ctr.Token,
			Version:  "test",
			Client:   gClient,
			BindPort: bindPort,
			WorkDir:  dir,
			Session:  sess,
			S3Bucket: bucket,
		})

		bindPort++

		require.NoError(t, err)

		ctx, cancel := context.WithCancel(ctx)

		defer cancel()

		go client.Run(ctx)

		// Wire it up to the servers serf config
		_, err = client.s.Join([]string{"127.0.0.1:25000"}, true)
		require.NoError(t, err)

		time.Sleep(time.Second)

		// Setup the info so that we're tracking the account when the event arrives
		client.accountServices[accountId.SpecString()] = &accountInfo{
			MapKey:   accountId.SpecString(),
			S3Key:    "account_services/" + accountId.SpecString(),
			FileName: accountId.SpecString(),
			Process:  make(chan struct{}),
		}

		serviceId := pb.NewULID()
		labels := pb.ParseLabelSet("service=www,env=prod")

		servReq := &pb.ServiceRequest{
			Account: &pb.Account{
				Namespace: "/",
				AccountId: accountId,
			},
			Id:        serviceId,
			Hub:       pb.NewULID(),
			Type:      "test",
			LabelSets: []*pb.LabelSet{labels},
			Metadata: []*pb.KVPair{
				{
					Key:   "version",
					Value: "0.1x",
				},
			},
		}

		_, err = gClient.AddService(ctx, servReq)
		require.NoError(t, err)

		var so Service
		err = dbx.Check(db.First(&so))
		require.NoError(t, err)

		assert.Equal(t, serviceId.Bytes(), so.ServiceId)

		time.Sleep(time.Second)
		// Now, in the background, the client should have fetched account again

		services, err := client.LookupService(ctx, accountId, labels)
		require.NoError(t, err)

		require.Equal(t, 1, len(services))

		assert.Equal(t, serviceId, services[0].Id)
	})

	t.Run("resolves label links", func(t *testing.T) {
		bucket := os.Getenv("TEST_BUCKET")
		endpoint := os.Getenv("S3_ENDPOINT")
		if bucket == "" || endpoint == "" {
			t.Skip("no s3 bucket or creds")
		}

		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		var s Server
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"
		s.awsSess = sess
		s.bucket = bucket
		s.s = serfObj

		pub, err := token.SetupVault(vc, s.vaultPath)
		require.NoError(t, err)

		s.pubKey = pub

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

		label := pb.ParseLabelSet(":hostname=foo.com")
		target := pb.ParseLabelSet("service=www,env=prod")

		_, err = s.AddLabelLink(
			metadata.NewIncomingContext(top, md2),
			&pb.AddLabelLinkRequest{
				Labels: label,
				Account: &pb.Account{
					AccountId: accountId,
					Namespace: "/",
				},
				Target: target,
			},
		)

		require.NoError(t, err)

		// Check the labe link payload written to s3

		hubtoken, err := s.IssueHubToken(ctx, &pb.Noop{})
		require.NoError(t, err)

		id := pb.NewULID()

		dir, err := ioutil.TempDir("", "hzn")
		require.NoError(t, err)

		defer os.RemoveAll(dir)

		client, err := NewClient(ClientConfig{
			Id:       id,
			Token:    hubtoken.Token,
			Version:  "test",
			BindPort: bindPort,
			WorkDir:  dir,
			Session:  sess,
			S3Bucket: bucket,
		})

		bindPort++

		require.NoError(t, err)

		ctx, cancel := context.WithCancel(ctx)

		defer cancel()

		go client.Run(ctx)

		// Wire it up to the servers serf config
		_, err = client.s.Join([]string{"127.0.0.1:25000"}, true)
		require.NoError(t, err)

		time.Sleep(time.Second)

		labelAccount, labelTarget, err := client.ResolveLabelLink(label)
		require.NoError(t, err)

		assert.Equal(t, accountId, labelAccount)
		assert.Equal(t, target, labelTarget)
	})
}

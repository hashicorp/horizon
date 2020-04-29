package control

import (
	context "context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	fmt "fmt"
	"io/ioutil"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"cirello.io/dynamolock"
	"github.com/armon/go-metrics"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/grpc/lz4"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/testutils"
	"github.com/hashicorp/horizon/pkg/token"
	"github.com/jinzhu/gorm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestClient(t *testing.T) {
	testutils.SetupDB()

	vc := testutils.SetupVault()

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

	scfg := ServerConfig{
		VaultClient:   vc,
		VaultPath:     pb.NewULID().SpecString(),
		KeyId:         "k1",
		RegisterToken: "aabbcc",
		AwsSession:    sess,
		Bucket:        bucket,
		LockTable:     "hzntest",
	}

	t.Run("can create and remove a service", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		cfg := scfg
		cfg.DB = db

		s, err := NewServer(cfg)
		require.NoError(t, err)

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
		pb.RegisterControlServicesServer(gs, s)

		li, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		defer li.Close()

		go gs.Serve(li)

		gcc, err := grpc.Dial(li.Addr().String(),
			grpc.WithInsecure(),
			grpc.WithPerRPCCredentials(Token(ctr.Token)),
			grpc.WithDefaultCallOptions(grpc.UseCompressor(lz4.Name)),
		)

		require.NoError(t, err)

		defer gcc.Close()

		gClient := pb.NewControlServicesClient(gcc)

		id := pb.NewULID()

		client, err := NewClient(ctx, ClientConfig{
			Id:      id,
			Token:   ctr.Token,
			Version: "test",
			Client:  gClient,
			Session: sess,
		})

		require.NoError(t, err)

		serviceId := pb.NewULID()
		labels := pb.ParseLabelSet("service=www,env=prod")

		servReq := &pb.ServiceRequest{
			Account: &pb.Account{
				Namespace: "/",
				AccountId: accountId,
			},
			Id:     serviceId,
			Type:   "test",
			Labels: labels,
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

		ls, ok := client.localServices[serviceId.SpecString()]
		require.True(t, ok)

		assert.Equal(t, serviceId, ls.Id)

		err = client.RemoveService(ctx, servReq)
		require.NoError(t, err)

		_, ok = client.localServices[serviceId.SpecString()]
		require.False(t, ok)

		err = dbx.Check(db.First(&so))
		assert.Error(t, err)
	})

	t.Run("lookup a service locally and remotely", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		cfg := scfg
		cfg.DB = db

		s, err := NewServer(cfg)
		require.NoError(t, err)

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
		pb.RegisterControlServicesServer(gs, s)

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

		client, err := NewClient(ctx, ClientConfig{
			Id:       id,
			Token:    ctr.Token,
			Version:  "test",
			Client:   gClient,
			WorkDir:  dir,
			Session:  sess,
			S3Bucket: bucket,
		})

		require.NoError(t, err)

		serviceId := pb.NewULID()
		labels := pb.ParseLabelSet("service=www,env=prod,instance=xyz")

		servReq := &pb.ServiceRequest{
			Account: &pb.Account{
				Namespace: "/",
				AccountId: accountId,
			},
			Id:     serviceId,
			Type:   "test",
			Labels: labels,
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

		ls, ok := client.localServices[serviceId.SpecString()]
		require.True(t, ok)

		assert.Equal(t, serviceId, ls.Id)

		services, err := client.LookupService(ctx, accountId, pb.ParseLabelSet("service=www,env=prod"))
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
				Hub:    hubId2,
				Id:     serviceId2,
				Type:   "test",
				Labels: labels,
				Metadata: []*pb.KVPair{
					{
						Key:   "version",
						Value: "0.1x",
					},
				},
			},
		)

		require.NoError(t, err)

		services, err = client.LookupService(ctx, accountId, pb.ParseLabelSet("service=www,env=prod"))
		require.NoError(t, err)

		require.Equal(t, 2, len(services))

		assert.Equal(t, serviceId, services[0].Id)
		assert.Equal(t, serviceId2, services[1].Id)
	})

	t.Run("refreshes an account on response to activity from the server", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		cfg := scfg
		cfg.DB = db

		s, err := NewServer(cfg)
		require.NoError(t, err)

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
		pb.RegisterControlServicesServer(gs, s)

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

		client, err := NewClient(ctx, ClientConfig{
			Id:       id,
			Token:    ctr.Token,
			Version:  "test",
			Client:   gClient,
			WorkDir:  dir,
			Session:  sess,
			S3Bucket: bucket,
		})

		require.NoError(t, err)

		ctx, cancel := context.WithCancel(ctx)

		defer cancel()

		go client.Run(ctx)

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
			Id:     serviceId,
			Hub:    pb.NewULID(),
			Type:   "test",
			Labels: labels,
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

		services, err := client.LookupService(ctx, accountId, labels)
		require.NoError(t, err)

		require.Equal(t, 1, len(services))

		assert.Equal(t, serviceId, services[0].Id)
	})

	t.Run("resolves label links", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		cfg := scfg
		cfg.DB = db

		s, err := NewServer(cfg)
		require.NoError(t, err)

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

		client, err := NewClient(ctx, ClientConfig{
			Id:       id,
			Token:    hubtoken.Token,
			Version:  "test",
			WorkDir:  dir,
			Session:  sess,
			S3Bucket: bucket,
		})

		require.NoError(t, err)

		ctx, cancel := context.WithCancel(ctx)

		defer cancel()

		go client.Run(ctx)

		time.Sleep(time.Second)

		labelAccount, labelTarget, err := client.ResolveLabelLink(label)
		require.NoError(t, err)

		assert.Equal(t, accountId, labelAccount)
		assert.Equal(t, target, labelTarget)
	})

	t.Run("bootstraps configuration from the server", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		cfg := scfg
		cfg.DB = db

		s, err := NewServer(cfg)
		require.NoError(t, err)

		tlspub, tlspriv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)

		notBefore := time.Now()

		serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
		serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
		require.NoError(t, err)

		template := x509.Certificate{
			SerialNumber: serialNumber,
			Subject: pkix.Name{
				Organization: []string{"Acme Co"},
			},
			NotBefore: time.Now(),
			NotAfter:  notBefore.Add(5 * time.Minute),

			KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
			ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			BasicConstraintsValid: true,
			DNSNames:              []string{"hub.test"},
			IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
			IsCA:                  true,
		}

		derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, tlspub, tlspriv)
		require.NoError(t, err)

		s.hubCert = derBytes
		s.hubKey = tlspriv

		s.lockMgr, err = dynamolock.New(dynamodb.New(sess), s.lockTable)
		require.NoError(t, err)

		_, err = s.lockMgr.CreateTable(s.lockTable)
		require.NoError(t, err)

		pub, err := token.SetupVault(vc, s.vaultPath)
		require.NoError(t, err)

		s.pubKey = pub

		top, cancel := context.WithCancel(context.Background())
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

		ctr, err := s.IssueHubToken(ctx, &pb.Noop{})
		require.NoError(t, err)

		gs := grpc.NewServer()
		pb.RegisterControlServicesServer(gs, s)

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

		client, err := NewClient(ctx, ClientConfig{
			Id:      id,
			Token:   ctr.Token,
			Version: "test",
			Client:  gClient,
			Session: sess,
		})

		require.NoError(t, err)

		err = client.BootstrapConfig(ctx)

		cli, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		defer cli.Close()

		tlsPort := cli.Addr().(*net.TCPAddr).Port

		go client.RunIngress(ctx, cli, nil)

		parsedHubCert, err := x509.ParseCertificate(derBytes)
		require.NoError(t, err)

		var tlscfg tls.Config

		tlscfg.RootCAs = x509.NewCertPool()
		tlscfg.RootCAs.AddCert(parsedHubCert)

		httpc := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tlscfg,
			},
		}

		resp, err := httpc.Get(fmt.Sprintf("https://127.0.0.1:%d/_healthz", tlsPort))
		require.NoError(t, err)
		assert.Equal(t, 200, resp.StatusCode)
	})

	t.Run("transmit a flow record up to the server", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		cfg := scfg
		cfg.DB = db

		s, err := NewServer(cfg)
		require.NoError(t, err)

		top, cancel := context.WithCancel(context.Background())
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

		gs := grpc.NewServer()
		pb.RegisterControlServicesServer(gs, s)

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

		client, err := NewClient(ctx, ClientConfig{
			Id:      id,
			Token:   ctr.Token,
			Version: "test",
			Client:  gClient,
			Session: sess,
		})

		require.NoError(t, err)

		go client.Run(ctx)

		hubId := pb.NewULID()
		agentId := pb.NewULID()
		serviceId := pb.NewULID()
		flowId := pb.NewULID()

		client.SendFlow(&pb.FlowRecord{
			Stream: &pb.FlowStream{
				FlowId:      flowId,
				StartedAt:   pb.NewTimestamp(time.Now()),
				HubId:       hubId,
				AgentId:     agentId,
				AccountId:   accountId,
				ServiceId:   serviceId,
				NumMessages: 55,
				NumBytes:    113332,
			},
		})

		time.Sleep(time.Second)

		data := s.msink.(*metrics.InmemSink).Data()

		labels := strings.Join([]string{
			"flow=" + flowId.SpecString(),
			"hub=" + hubId.SpecString(),
			"agent=" + agentId.SpecString(),
			"service=" + serviceId.SpecString(),
			"account=" + accountId.SpecString(),
		}, ";")

		assert.Equal(t, int64(55), int64(data[0].Counters["control-server.stream.messages;"+labels].Sum))
		assert.Equal(t, int64(113332), int64(data[0].Counters["control-server.stream.bytes;"+labels].Sum))

		client.SendFlow(&pb.FlowRecord{
			Stream: &pb.FlowStream{
				FlowId:      flowId,
				StartedAt:   pb.NewTimestamp(time.Now()),
				HubId:       hubId,
				AgentId:     agentId,
				AccountId:   accountId,
				ServiceId:   serviceId,
				Labels:      pb.ParseLabelSet("service=echo"),
				NumMessages: 5,
				NumBytes:    8,
			},
		})

		time.Sleep(time.Second)

		data = s.msink.(*metrics.InmemSink).Data()

		assert.Equal(t, int64(60), int64(data[0].Counters["control-server.stream.messages;"+labels].Sum))
		assert.Equal(t, int64(113340), int64(data[0].Counters["control-server.stream.bytes;"+labels].Sum))

		s.opsToken = "opsrocks"

		mdops := metadata.MD{}
		mdops.Set("authorization", "xyz")

		opsctx := metadata.NewIncomingContext(ctx, mdops)

		snap, err := s.CurrentFlowTop(opsctx, &pb.FlowTopRequest{})
		require.NoError(t, err)

		require.Equal(t, 1, len(snap.Records))

		assert.Equal(t, flowId, snap.Records[0].FlowId)
	})

	t.Run("can get a list of all hubs and locations", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		cfg := scfg
		cfg.DB = db

		s, err := NewServer(cfg)
		require.NoError(t, err)

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

		ctr, err := s.IssueHubToken(ctx, &pb.Noop{})
		require.NoError(t, err)

		gs := grpc.NewServer()
		pb.RegisterControlServicesServer(gs, s)

		li, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		defer li.Close()

		go gs.Serve(li)

		gcc, err := grpc.Dial(li.Addr().String(),
			grpc.WithInsecure(),
			grpc.WithPerRPCCredentials(Token(ctr.Token)),
			grpc.WithDefaultCallOptions(grpc.UseCompressor(lz4.Name)),
		)

		require.NoError(t, err)

		defer gcc.Close()

		gClient := pb.NewControlServicesClient(gcc)

		id := pb.NewULID()

		client, err := NewClient(ctx, ClientConfig{
			Id:      id,
			Token:   ctr.Token,
			Version: "test",
			Client:  gClient,
			Session: sess,
		})

		require.NoError(t, err)

		netlocs := []*pb.NetworkLocation{
			{
				Addresses: []string{"1.1.1.1"},
				Labels:    pb.ParseLabelSet("dc=test"),
			},
		}

		data, err := json.Marshal(netlocs)
		require.NoError(t, err)

		hubId := pb.NewULID()

		var hr Hub
		hr.StableID = pb.NewULID().Bytes()
		hr.InstanceID = hubId.Bytes()
		hr.ConnectionInfo = data
		hr.LastCheckin = time.Now()
		hr.CreatedAt = time.Now()

		err = dbx.Check(s.db.Create(&hr))

		hubs, err := client.AllHubs(ctx)
		require.NoError(t, err)

		require.Equal(t, 1, len(hubs))

		assert.Equal(t, hubId, hubs[0].Id)

	})

	t.Run("removes a old hubs services connecting with the same stable id", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		cfg := scfg
		cfg.DB = db

		s, err := NewServer(cfg)
		require.NoError(t, err)

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

		ctr, err := s.IssueHubToken(ctx, &pb.Noop{})
		require.NoError(t, err)

		gs := grpc.NewServer()
		pb.RegisterControlServicesServer(gs, s)

		li, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		defer li.Close()

		go gs.Serve(li)

		gcc, err := grpc.Dial(li.Addr().String(),
			grpc.WithInsecure(),
			grpc.WithPerRPCCredentials(Token(ctr.Token)),
			grpc.WithDefaultCallOptions(grpc.UseCompressor(lz4.Name)),
		)

		require.NoError(t, err)

		defer gcc.Close()

		gClient := pb.NewControlServicesClient(gcc)

		id := pb.NewULID()

		client, err := NewClient(ctx, ClientConfig{
			Id:      id,
			Token:   ctr.Token,
			Version: "test",
			Client:  gClient,
			Session: sess,
		})

		require.NoError(t, err)

		prev := pb.NewULID()

		var hr Hub
		hr.StableID = id.Bytes()
		hr.InstanceID = prev.Bytes()
		hr.ConnectionInfo = []byte("{}")

		err = dbx.Check(db.Create(&hr))
		require.NoError(t, err)

		var so Service
		so.HubId = prev.Bytes()
		so.Labels = pb.ParseLabelSet("test=env").AsStringArray()

		err = dbx.Check(db.Create(&so))
		require.NoError(t, err)

		err = client.BootstrapConfig(ctx)
		require.NoError(t, err)

		var so2 Service
		err = dbx.Check(db.First(&so2))

		assert.Error(t, err)

		var hr2 Hub

		err = dbx.Check(db.First(&hr2))
		require.NoError(t, err)

		assert.Equal(t, hr2.InstanceID, client.Id().Bytes())
	})

}

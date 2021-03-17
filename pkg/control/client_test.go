package control

import (
	bytes "bytes"
	context "context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	fmt "fmt"
	"io/ioutil"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/armon/go-metrics"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/internal/testsql"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/grpc/lz4"
	grpctoken "github.com/hashicorp/horizon/pkg/grpc/token"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/testutils"
	"github.com/hashicorp/horizon/pkg/token"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestClient(t *testing.T) {
	vc := testutils.SetupVault()
	sess := testutils.AWSSession(t)

	bucket := "hzntest-" + strings.ToLower(pb.NewULID().SpecString())
	_, err := s3.New(sess).CreateBucket(&s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	require.NoError(t, err)

	defer testutils.DeleteBucket(s3.New(sess), bucket)

	scfg := ServerConfig{
		VaultClient:       vc,
		VaultPath:         pb.NewULID().SpecString(),
		KeyId:             "k1",
		RegisterToken:     "aabbcc",
		AwsSession:        sess,
		Bucket:            bucket,
		DisablePrometheus: true,
	}

	t.Run("can create and remove a service", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "periodic")
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

		account := &pb.Account{
			AccountId: pb.NewULID(),
			Namespace: "/",
		}

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
			grpc.WithPerRPCCredentials(grpctoken.Token(ctr.Token)),
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
			Account: account,
			Id:      serviceId,
			Type:    "test",
			Labels:  labels,
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
		db := testsql.TestPostgresDB(t, "periodic")
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

		account := &pb.Account{
			AccountId: pb.NewULID(),
			Namespace: "/",
		}

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
			grpc.WithPerRPCCredentials(grpctoken.Token(ctr.Token)))

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
			Account: account,
			Hub:     pb.NewULID(),
			Id:      serviceId,
			Type:    "test",
			Labels:  labels,
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

		calc, err := client.LookupService(ctx, account, pb.ParseLabelSet("service=www,env=prod"))
		require.NoError(t, err)

		services := calc.Services()

		assert.Equal(t, 1, len(services))

		assert.Equal(t, serviceId, services[0].Id)

		hubId2 := pb.NewULID()
		serviceId2 := pb.NewULID()
		serviceId3 := pb.NewULID()

		// Nuke to force an inline refresh
		delete(client.accountServices, account.SpecString())

		hubtoken, err := s.IssueHubToken(ctx, &pb.Noop{})
		require.NoError(t, err)

		md3 := make(metadata.MD)
		md3.Set("authorization", hubtoken.Token)

		_, err = s.AddService(
			metadata.NewIncomingContext(top, md3),
			&pb.ServiceRequest{
				Account: account,
				Hub:     pb.NewULID(),
				Id:      serviceId2,
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

		_, err = s.AddService(
			metadata.NewIncomingContext(top, md3),
			&pb.ServiceRequest{
				Account: account,
				Hub:     hubId2,
				Id:      serviceId3,
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

		calc, err = client.LookupService(ctx, account, pb.ParseLabelSet("service=www,env=prod"))
		require.NoError(t, err)

		services = calc.All

		require.Equal(t, 3, len(services))

		assert.Equal(t, serviceId, services[0].Id)
		assert.Equal(t, serviceId2, services[1].Id)
		assert.Equal(t, serviceId3, services[2].Id)

		var notId int

		for i := 0; i < 20; i++ {
			serv := calc.Services()

			assert.Equal(t, serviceId, serv[0].Id, "local service is always first")

			if !calc.Services()[1].Id.Equal(serviceId2) {
				notId++
			}
		}

		assert.True(t, notId > 2)
		assert.True(t, notId < 18)
	})

	t.Run("honors deployment-order", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "periodic")
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

		account := &pb.Account{
			AccountId: pb.NewULID(),
			Namespace: "/",
		}

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
			grpc.WithPerRPCCredentials(grpctoken.Token(ctr.Token)))

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
		labels := pb.ParseLabelSet("service=www,env=prod,instance=xyz,:deployment-order=a")

		servReq := &pb.ServiceRequest{
			Account: account,
			Id:      serviceId,
			Type:    "test",
			Labels:  labels,
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

		calc, err := client.LookupService(ctx, account, pb.ParseLabelSet("service=www,env=prod"))
		require.NoError(t, err)

		services := calc.Services()

		assert.Equal(t, 1, len(services))

		assert.Equal(t, serviceId, services[0].Id)

		hubId2 := pb.NewULID()
		serviceId2 := pb.NewULID()

		// Nuke to force an inline refresh
		delete(client.accountServices, account.SpecString())

		hubtoken, err := s.IssueHubToken(ctx, &pb.Noop{})
		require.NoError(t, err)

		md3 := make(metadata.MD)
		md3.Set("authorization", hubtoken.Token)

		labels = pb.ParseLabelSet("service=www,env=prod,instance=xyz,:deployment-order=b")

		_, err = s.AddService(
			metadata.NewIncomingContext(top, md3),
			&pb.ServiceRequest{
				Account: account,
				Hub:     hubId2,
				Id:      serviceId2,
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

		calc, err = client.LookupService(ctx, account, pb.ParseLabelSet("service=www,env=prod"))
		require.NoError(t, err)

		services = calc.Best

		require.Equal(t, 1, len(services))

		assert.Equal(t, serviceId2, services[0].Id)

		services = calc.Services()

		require.Equal(t, 1, len(services))

		assert.Equal(t, serviceId2, services[0].Id)
	})

	t.Run("refreshes an account on response to activity from the server", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "periodic")
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

		account := &pb.Account{
			AccountId: pb.NewULID(),
			Namespace: "/",
		}

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
			grpc.WithPerRPCCredentials(grpctoken.Token(ctr.Token)))

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
		client.accountServices[account.StringKey()] = &accountInfo{
			MapKey:   account.StringKey(),
			S3Key:    "account_services/" + account.HashKey(),
			FileName: account.StringKey(),
			Process:  make(chan struct{}),
		}

		serviceId := pb.NewULID()
		labels := pb.ParseLabelSet("service=www,env=prod")

		hubId := pb.NewULID()

		servReq := &pb.ServiceRequest{
			Account: account,
			Id:      serviceId,
			Hub:     hubId,
			Type:    "test",
			Labels:  labels,
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

		calc, err := client.LookupService(ctx, account, labels)
		require.NoError(t, err)

		services := calc.Services()

		require.Equal(t, 1, len(services))

		assert.Equal(t, serviceId, services[0].Id)

		// Inject an event that will make the hub as unavailable

		client.processCentralActivity(ctx, client.L, &pb.CentralActivity{
			HubChange: &pb.HubChange{
				OldId: hubId,
			},
		})

		calc, err = client.LookupService(ctx, account, labels)
		require.NoError(t, err)

		services = calc.Services()

		require.Equal(t, 0, len(services))
	})

	t.Run("ignores records from hubs reported as not alive", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "periodic")
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

		account := &pb.Account{
			AccountId: pb.NewULID(),
			Namespace: "/",
		}

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
			grpc.WithPerRPCCredentials(grpctoken.Token(ctr.Token)))

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
		client.accountServices[account.StringKey()] = &accountInfo{
			MapKey:   account.StringKey(),
			S3Key:    "account_services/" + account.HashKey(),
			FileName: account.StringKey(),
			Process:  make(chan struct{}),
		}

		serviceId := pb.NewULID()
		labels := pb.ParseLabelSet("service=www,env=prod")

		servReq := &pb.ServiceRequest{
			Account: account,
			Id:      serviceId,
			Hub:     pb.NewULID(),
			Type:    "test",
			Labels:  labels,
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

		calc, err := client.LookupService(ctx, account, labels)
		require.NoError(t, err)

		services := calc.Services()

		require.Equal(t, 1, len(services))

		assert.Equal(t, serviceId, services[0].Id)
	})

	t.Run("resolves label links", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "periodic")
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

		account := &pb.Account{
			AccountId: pb.NewULID(),
			Namespace: "/",
		}

		label := pb.ParseLabelSet(":hostname=foo.com")
		target := pb.ParseLabelSet("service=www,env=prod")

		_, err = s.AddAccount(
			metadata.NewIncomingContext(top, md2),
			&pb.AddAccountRequest{
				Account: account,
			},
		)

		require.NoError(t, err)

		_, err = s.AddLabelLink(
			metadata.NewIncomingContext(top, md2),
			&pb.AddLabelLinkRequest{
				Labels:  label,
				Account: account,
				Target:  target,
			},
		)

		require.NoError(t, err)

		// Check the labe link payload written to s3
		time.Sleep(2 * time.Second)

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

		labelAccount, labelTarget, _, err := client.ResolveLabelLink(label)
		require.NoError(t, err)

		assert.Equal(t, account, labelAccount)
		assert.Equal(t, target, labelTarget)
	})

	t.Run("bootstraps configuration from the server", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "periodic")
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

		var certBuf, keyBuf bytes.Buffer

		pem.Encode(&certBuf, &pem.Block{
			Type:  "CERTIFICATE",
			Bytes: derBytes,
		})

		keybytes, err := x509.MarshalPKCS8PrivateKey(tlspriv)
		require.NoError(t, err)

		pem.Encode(&keyBuf, &pem.Block{
			Type:  "PRIVATE KEY",
			Bytes: keybytes,
		})

		s.hubCert = certBuf.Bytes()
		s.hubKey = keyBuf.Bytes()

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
			grpc.WithPerRPCCredentials(grpctoken.Token(ctr.Token)))

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

		hh := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		})

		go func() {
			err := client.RunIngress(ctx, cli, nil, hh)
			if err != nil {
				hclog.L().Error("error running ingress", "error", err)
			}
		}()

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
		db := testsql.TestPostgresDB(t, "periodic")
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

		account := &pb.Account{
			AccountId: pb.NewULID(),
			Namespace: "/",
		}

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
			grpc.WithPerRPCCredentials(grpctoken.Token(ctr.Token)))

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
				Account:     account,
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
			"account=" + account.SpecString(),
		}, ";")

		require.True(t, len(data) > 0)

		sample, ok := data[0].Counters["control.stream.messages;"+labels]
		require.True(t, ok)

		assert.Equal(t, int64(55), int64(sample.Sum))

		sample, ok = data[0].Counters["control.stream.bytes;"+labels]
		require.True(t, ok)

		assert.Equal(t, int64(113332), int64(sample.Sum))

		client.SendFlow(&pb.FlowRecord{
			Stream: &pb.FlowStream{
				FlowId:      flowId,
				StartedAt:   pb.NewTimestamp(time.Now()),
				HubId:       hubId,
				AgentId:     agentId,
				Account:     account,
				ServiceId:   serviceId,
				Labels:      pb.ParseLabelSet("service=echo"),
				NumMessages: 5,
				NumBytes:    8,
			},
		})

		time.Sleep(time.Second)

		data = s.msink.(*metrics.InmemSink).Data()

		assert.Equal(t, int64(60), int64(data[0].Counters["control.stream.messages;"+labels].Sum))
		assert.Equal(t, int64(113340), int64(data[0].Counters["control.stream.bytes;"+labels].Sum))

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
		db := testsql.TestPostgresDB(t, "periodic")
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
			grpc.WithPerRPCCredentials(grpctoken.Token(ctr.Token)),
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
		db := testsql.TestPostgresDB(t, "periodic")
		defer db.Close()

		cfg := scfg
		cfg.DB = db

		s, err := NewServer(cfg)
		require.NoError(t, err)

		cert, key, err := testutils.SelfSignedCert()
		require.NoError(t, err)

		s.hubCert = cert
		s.hubKey = key

		top := context.Background()

		md := make(metadata.MD)
		md.Set("authorization", "aabbcc")

		ctx, cancel := context.WithCancel(metadata.NewIncomingContext(top, md))
		defer cancel()

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
			grpc.WithPerRPCCredentials(grpctoken.Token(ctr.Token)),
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

		// Setup another client so we can track the hub id change
		// notification.
		c2, err := NewClient(ctx, ClientConfig{
			Id:      pb.NewULID(),
			Token:   ctr.Token,
			Version: "test",
			Client:  gClient,
			Session: sess,
		})

		require.NoError(t, err)

		go c2.Run(ctx)

		prev := pb.NewULID()

		var hr Hub
		hr.StableID = id.Bytes()
		hr.InstanceID = prev.Bytes()
		hr.ConnectionInfo = []byte("{}")

		err = dbx.Check(db.Create(&hr))
		require.NoError(t, err)

		var so Service
		so.HubId = prev.Bytes()
		so.AccountId = (&pb.Account{
			AccountId: pb.NewULID(),
			Namespace: "/",
		}).Key()

		so.Labels = pb.ParseLabelSet("test=env").AsStringArray()

		err = dbx.Check(db.Create(&so))
		require.NoError(t, err)

		err = client.BootstrapConfig(ctx)
		require.NoError(t, err)

		// Database cleanup is in the background
		time.Sleep(time.Second)

		var so2 Service
		err = dbx.Check(db.First(&so2))

		assert.Error(t, err)

		var hr2 Hub

		err = dbx.Check(db.First(&hr2))
		require.NoError(t, err)

		assert.Equal(t, hr2.InstanceID, client.Id().Bytes())

		lv, ok := c2.liveHubs.Get(prev.SpecString())
		require.True(t, ok)
		assert.False(t, lv.(*hubLiveness).alive)

		lv, ok = c2.liveHubs.Get(client.Id().SpecString())
		require.True(t, ok)
		assert.True(t, lv.(*hubLiveness).alive)
	})

	t.Run("reconnects the activity stream if disconnected", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "periodic")
		defer db.Close()

		cfg := scfg
		cfg.DB = db

		s, err := NewServer(cfg)
		require.NoError(t, err)

		cert, key, err := testutils.SelfSignedCert()
		require.NoError(t, err)

		s.hubCert = cert
		s.hubKey = key

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

		account := &pb.Account{
			AccountId: pb.NewULID(),
			Namespace: "/",
		}

		ctr, err := s.IssueHubToken(ctx, &pb.Noop{})
		require.NoError(t, err)

		gs := grpc.NewServer()
		pb.RegisterControlServicesServer(gs, s)

		li, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		defer li.Close()

		go gs.Serve(li)

		id := pb.NewULID()

		dir, err := ioutil.TempDir("", "hzn")
		require.NoError(t, err)

		defer os.RemoveAll(dir)

		client, err := NewClient(ctx, ClientConfig{
			Id:       id,
			Token:    ctr.Token,
			Version:  "test",
			WorkDir:  dir,
			Session:  sess,
			S3Bucket: bucket,

			Addr:     li.Addr().String(),
			Insecure: true,
		})

		require.NoError(t, err)

		ctx, cancel := context.WithCancel(ctx)

		defer cancel()

		go client.Run(ctx)

		time.Sleep(time.Second)

		li.Close()

		li, err = net.Listen("tcp", li.Addr().String())
		require.NoError(t, err)

		defer li.Close()

		go gs.Serve(li)

		// Setup the info so that we're tracking the account when the event arrives
		client.accountServices[account.StringKey()] = &accountInfo{
			MapKey:   account.StringKey(),
			S3Key:    "account_services/" + account.HashKey(),
			FileName: account.StringKey(),
			Process:  make(chan struct{}),
		}

		serviceId := pb.NewULID()
		labels := pb.ParseLabelSet("service=www,env=prod")

		servReq := &pb.ServiceRequest{
			Account: account,
			Id:      serviceId,
			Hub:     pb.NewULID(),
			Type:    "test",
			Labels:  labels,
			Metadata: []*pb.KVPair{
				{
					Key:   "version",
					Value: "0.1x",
				},
			},
		}

		_, err = client.client.AddService(ctx, servReq)
		require.NoError(t, err)

		var so Service
		err = dbx.Check(db.First(&so))
		require.NoError(t, err)

		assert.Equal(t, serviceId.Bytes(), so.ServiceId)

		calc, err := client.LookupService(ctx, account, labels)
		require.NoError(t, err)

		services := calc.Services()

		require.Equal(t, 1, len(services))

		assert.Equal(t, serviceId, services[0].Id)
	})

}

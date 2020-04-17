package control

import (
	context "context"
	"errors"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"cirello.io/dynamolock"
	"github.com/DATA-DOG/go-txdb"
	"github.com/DataDog/zstd"
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

func TestServer(t *testing.T) {
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

	bucket := "hzntest"
	s3.New(sess).DeleteBucket(&s3.DeleteBucketInput{
		Bucket: aws.String(bucket),
	})
	s3.New(sess).CreateBucket(&s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})

	serfcfg := serf.DefaultConfig()
	serfcfg.NodeName = pb.NewULID().SpecString()
	serfObj, err := serf.Create(serfcfg)
	require.NoError(t, err)
	defer serfObj.Shutdown()

	t.Run("can register a new management client", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		var s Server
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"

		pub, err := token.SetupVault(vc, s.vaultPath)
		require.NoError(t, err)

		ctx := context.Background()

		md := make(metadata.MD)
		md.Set("authorization", "aabbcc")

		ctx = metadata.NewIncomingContext(ctx, md)

		ct, err := s.Register(ctx, &pb.ControlRegister{
			Namespace: "/",
		})

		require.NoError(t, err)

		ht, err := token.CheckTokenED25519(ct.Token, pub)
		require.NoError(t, err)

		assert.Equal(t, pb.MANAGE, ht.Body.Role)
	})

	t.Run("rejects register requests with the wrong register token", func(t *testing.T) {
		var s Server
		s.registerToken = "aabbcc"

		ctx := context.Background()

		_, err := s.Register(ctx, &pb.ControlRegister{
			Namespace: "/",
		})

		require.Error(t, err)
		assert.True(t, errors.Is(ErrBadAuthentication, err))

		md := make(metadata.MD)
		md.Set("authorization", "xyz")

		ctx2 := metadata.NewIncomingContext(ctx, md)

		_, err = s.Register(ctx2, &pb.ControlRegister{
			Namespace: "/",
		})

		require.Error(t, err)
		assert.True(t, errors.Is(ErrBadAuthentication, err))
	})

	t.Run("can create a new agent token using a management token", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		var s Server
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"

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

		ctr, err := s.CreateToken(
			metadata.NewIncomingContext(top, md2),
			&pb.CreateTokenRequest{
				Account: &pb.Account{
					Namespace: "/",
					AccountId: accountId,
				},
				Capabilities: []pb.TokenCapability{
					{
						Capability: pb.SERVE,
					},
				},
				ValidDuration: pb.TimestampFromDuration(6 * time.Hour),
			},
		)
		require.NoError(t, err)

		ht, err := token.CheckTokenED25519(ctr.Token, pub)
		require.NoError(t, err)

		assert.Equal(t, 1, len(ht.Body.Capabilities))

		ok, _ := ht.HasCapability(token.CapaServe)
		require.True(t, ok)
	})

	t.Run("disallows creating an agent token in a different namespace", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		var s Server
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"

		pub, err := token.SetupVault(vc, s.vaultPath)
		require.NoError(t, err)

		s.pubKey = pub

		top := context.Background()

		md := make(metadata.MD)
		md.Set("authorization", "aabbcc")

		ctx := metadata.NewIncomingContext(top, md)

		ct, err := s.Register(ctx, &pb.ControlRegister{
			Namespace: "/foo",
		})

		require.NoError(t, err)

		md2 := make(metadata.MD)
		md2.Set("authorization", ct.Token)

		accountId := pb.NewULID()

		_, err = s.CreateToken(
			metadata.NewIncomingContext(top, md2),
			&pb.CreateTokenRequest{
				Account: &pb.Account{
					Namespace: "/bar",
					AccountId: accountId,
				},
				Capabilities: []pb.TokenCapability{
					{
						Capability: pb.SERVE,
					},
				},
				ValidDuration: pb.TimestampFromDuration(6 * time.Hour),
			},
		)
		require.Error(t, err)
	})

	t.Run("allows creating a token in a sub-namespace", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		var s Server
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"

		pub, err := token.SetupVault(vc, s.vaultPath)
		require.NoError(t, err)

		s.pubKey = pub

		top := context.Background()

		md := make(metadata.MD)
		md.Set("authorization", "aabbcc")

		ctx := metadata.NewIncomingContext(top, md)

		ct, err := s.Register(ctx, &pb.ControlRegister{
			Namespace: "/foo",
		})

		require.NoError(t, err)

		md2 := make(metadata.MD)
		md2.Set("authorization", ct.Token)

		accountId := pb.NewULID()

		ctr, err := s.CreateToken(
			metadata.NewIncomingContext(top, md2),
			&pb.CreateTokenRequest{
				Account: &pb.Account{
					Namespace: "/foo/bar",
					AccountId: accountId,
				},
				Capabilities: []pb.TokenCapability{
					{
						Capability: pb.SERVE,
					},
				},
				ValidDuration: pb.TimestampFromDuration(6 * time.Hour),
			},
		)
		require.NoError(t, err)

		ht, err := token.CheckTokenED25519(ctr.Token, pub)
		require.NoError(t, err)

		assert.Equal(t, 1, len(ht.Body.Capabilities))

		ok, _ := ht.HasCapability(token.CapaServe)
		require.True(t, ok)
	})

	t.Run("disallows creating an agent token in a common prefix but without separater", func(t *testing.T) {
		db, err := gorm.Open("pgtest", "server")
		require.NoError(t, err)

		defer db.Close()

		var s Server
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"

		pub, err := token.SetupVault(vc, s.vaultPath)
		require.NoError(t, err)

		s.pubKey = pub

		top := context.Background()

		md := make(metadata.MD)
		md.Set("authorization", "aabbcc")

		ctx := metadata.NewIncomingContext(top, md)

		ct, err := s.Register(ctx, &pb.ControlRegister{
			Namespace: "/foo",
		})

		require.NoError(t, err)

		md2 := make(metadata.MD)
		md2.Set("authorization", ct.Token)

		accountId := pb.NewULID()

		_, err = s.CreateToken(
			metadata.NewIncomingContext(top, md2),
			&pb.CreateTokenRequest{
				Account: &pb.Account{
					Namespace: "/foobar",
					AccountId: accountId,
				},
				Capabilities: []pb.TokenCapability{
					{
						Capability: pb.SERVE,
					},
				},
				ValidDuration: pb.TimestampFromDuration(6 * time.Hour),
			},
		)
		require.Error(t, err)
	})

	t.Run("can create and remove a labellink for an account", func(t *testing.T) {
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

		s3api := s3.New(sess)

		resp, err := s3api.GetObject(&s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String("label_links"),
		})

		require.NoError(t, err)

		compressedData, err := ioutil.ReadAll(resp.Body)
		require.NoError(t, err)

		data, err := zstd.Decompress(nil, compressedData)
		require.NoError(t, err)

		var lls pb.LabelLinks

		err = lls.Unmarshal(data)
		require.NoError(t, err)

		require.Equal(t, 1, len(lls.LabelLinks))

		ll := lls.LabelLinks[0]

		assert.Equal(t, accountId, ll.Account.AccountId)
		assert.Equal(t, label, ll.Labels)
		assert.Equal(t, target, ll.Target)

		_, err = s.RemoveLabelLink(
			metadata.NewIncomingContext(top, md2),
			&pb.RemoveLabelLinkRequest{
				Labels: label,
				Account: &pb.Account{
					AccountId: accountId,
					Namespace: "/",
				},
			},
		)
		require.NoError(t, err)

		var llr LabelLink
		err = dbx.Check(db.First(&llr))

		assert.Error(t, err)

		resp, err = s3api.GetObject(&s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String("label_links"),
		})

		require.NoError(t, err)

		compressedData, err = ioutil.ReadAll(resp.Body)
		require.NoError(t, err)

		data, err = zstd.Decompress(nil, compressedData)
		require.NoError(t, err)

		var lls2 pb.LabelLinks

		err = lls2.Unmarshal(data)
		require.NoError(t, err)

		require.Equal(t, 0, len(lls2.LabelLinks))
	})

	t.Run("can create and remove a service for an account", func(t *testing.T) {
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

		md3 := make(metadata.MD)
		md3.Set("authorization", ctr.Token)

		labels := pb.ParseLabelSet("service=www,env=prod")

		hubId := pb.NewULID()
		serviceId := pb.NewULID()

		_, err = s.AddService(
			metadata.NewIncomingContext(top, md3),
			&pb.ServiceRequest{
				Account: &pb.Account{
					Namespace: "/",
					AccountId: accountId,
				},
				Hub:       hubId,
				Id:        serviceId,
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

		// Check the account payload written to s3

		s3api := s3.New(sess)

		resp, err := s3api.GetObject(&s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String("account_services/" + accountId.SpecString()),
		})

		require.NoError(t, err)

		compressedData, err := ioutil.ReadAll(resp.Body)
		require.NoError(t, err)

		data, err := zstd.Decompress(nil, compressedData)
		require.NoError(t, err)

		var accs pb.AccountServices

		err = accs.Unmarshal(data)
		require.NoError(t, err)

		require.Equal(t, 1, len(accs.Services))

		sr := accs.Services[0]

		assert.Equal(t, hubId, sr.Hub)
		assert.Equal(t, serviceId, sr.Id)
		require.Equal(t, 1, len(sr.LabelSets))
		assert.Equal(t, labels, sr.LabelSets[0])
		assert.Equal(t, "test", sr.Type)

		_, err = s.RemoveService(
			metadata.NewIncomingContext(top, md3),
			&pb.ServiceRequest{
				Account: &pb.Account{
					Namespace: "/",
					AccountId: accountId,
				},
				Hub: hubId,
				Id:  serviceId,
			},
		)
		require.NoError(t, err)

		var so Service
		err = dbx.Check(db.First(&so))

		assert.Error(t, err)

		resp, err = s3api.GetObject(&s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String("account_services/" + accountId.SpecString()),
		})

		require.NoError(t, err)

		compressedData, err = ioutil.ReadAll(resp.Body)
		require.NoError(t, err)

		data, err = zstd.Decompress(nil, compressedData)
		require.NoError(t, err)

		var accs2 pb.AccountServices

		err = accs2.Unmarshal(data)
		require.NoError(t, err)

		require.Equal(t, 0, len(accs2.Services))
	})

}

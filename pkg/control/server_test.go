package control

import (
	context "context"
	"errors"
	"io/ioutil"
	"testing"
	"time"

	"github.com/armon/go-metrics"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/internal/testsql"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/testutils"
	"github.com/hashicorp/horizon/pkg/token"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

func TestServer(t *testing.T) {
	vc := testutils.SetupVault()
	sess := testutils.AWSSession(t)

	bucket := "hzntest"

	s3.New(sess).CreateBucket(&s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})

	defer testutils.DeleteBucket(s3.New(sess), bucket)

	L := hclog.L()

	t.Run("can register a new management client", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "hzn")
		defer db.Close()

		var s Server
		s.L = L
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"

		s.m, _ = metrics.New(metrics.DefaultConfig("test"), &metrics.BlackholeSink{})

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
		s.L = L
		s.registerToken = "aabbcc"

		s.m, _ = metrics.New(metrics.DefaultConfig("test"), &metrics.BlackholeSink{})

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
		db := testsql.TestPostgresDB(t, "hzn")
		defer db.Close()

		var s Server
		s.L = L
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"

		s.m, _ = metrics.New(metrics.DefaultConfig("test"), &metrics.BlackholeSink{})

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

		ok, _ := ht.HasCapability(pb.SERVE)
		require.True(t, ok)
	})

	t.Run("disallows creating an agent token in a different namespace", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "hzn")
		defer db.Close()

		var s Server
		s.L = L
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"

		s.m, _ = metrics.New(metrics.DefaultConfig("test"), &metrics.BlackholeSink{})

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
		db := testsql.TestPostgresDB(t, "hzn")
		defer db.Close()

		var s Server
		s.L = L
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"

		s.m, _ = metrics.New(metrics.DefaultConfig("test"), &metrics.BlackholeSink{})

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

		ok, _ := ht.HasCapability(pb.SERVE)
		require.True(t, ok)
	})

	t.Run("disallows creating an agent token in a common prefix but without separater", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "hzn")
		defer db.Close()

		var s Server
		s.L = L
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"

		s.m, _ = metrics.New(metrics.DefaultConfig("test"), &metrics.BlackholeSink{})

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

	t.Run("can list all accounts in the namespace for a mgmt token", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "hzn")
		defer db.Close()

		var s Server
		s.L = L
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"

		s.m, _ = metrics.New(metrics.DefaultConfig("test"), &metrics.BlackholeSink{})

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
		accountId2 := pb.NewULID()
		accountId3 := pb.NewULID()

		mgmtCtx := metadata.NewIncomingContext(top, md2)

		_, err = s.CreateToken(
			mgmtCtx,
			&pb.CreateTokenRequest{
				Account: &pb.Account{
					Namespace: "/foo",
					AccountId: accountId,
				},
			},
		)
		require.NoError(t, err)

		_, err = s.CreateToken(
			mgmtCtx,
			&pb.CreateTokenRequest{
				Account: &pb.Account{
					Namespace: "/foo/bar",
					AccountId: accountId2,
				},
			},
		)
		require.NoError(t, err)

		ct2, err := s.Register(ctx, &pb.ControlRegister{
			Namespace: "/qux",
		})

		require.NoError(t, err)

		md3 := make(metadata.MD)
		md3.Set("authorization", ct2.Token)

		_, err = s.CreateToken(
			metadata.NewIncomingContext(top, md3),
			&pb.CreateTokenRequest{
				Account: &pb.Account{
					Namespace: "/qux",
					AccountId: accountId3,
				},
			},
		)
		require.NoError(t, err)

		list, err := s.ListAccounts(
			mgmtCtx,
			&pb.ListAccountsRequest{},
		)
		require.NoError(t, err)

		require.Equal(t, 2, len(list.Accounts))

		assert.Equal(t, accountId, list.Accounts[0].AccountId)
		assert.Equal(t, "/foo", list.Accounts[0].Namespace)
		assert.Equal(t, accountId2, list.Accounts[1].AccountId)
		assert.Equal(t, "/foo/bar", list.Accounts[1].Namespace)

		list, err = s.ListAccounts(
			mgmtCtx,
			&pb.ListAccountsRequest{Limit: 1},
		)
		require.NoError(t, err)

		require.Equal(t, 1, len(list.Accounts))

		assert.Equal(t, accountId, list.Accounts[0].AccountId)

		list, err = s.ListAccounts(
			mgmtCtx,
			&pb.ListAccountsRequest{
				Limit:  1,
				Marker: list.NextMarker,
			},
		)
		require.NoError(t, err)

		require.Equal(t, 1, len(list.Accounts))

		assert.Equal(t, accountId2, list.Accounts[0].AccountId)

		list, err = s.ListAccounts(
			mgmtCtx,
			&pb.ListAccountsRequest{
				Limit:  1,
				Marker: list.NextMarker,
			},
		)
		require.NoError(t, err)

		require.Equal(t, 0, len(list.Accounts))
	})

	t.Run("can create and remove a labellink for an account", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "hzn")
		defer db.Close()

		var s Server
		s.L = L
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"
		s.awsSess = sess
		s.bucket = bucket

		s.m, _ = metrics.New(metrics.DefaultConfig("test"), &metrics.BlackholeSink{})

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
		target := pb.ParseLabelSet("service=emp,env=test")

		_, err = s.AddAccount(
			metadata.NewIncomingContext(top, md2),
			&pb.AddAccountRequest{
				Account: &pb.Account{
					AccountId: accountId,
					Namespace: "/",
				},

				Limits: &pb.Account_Limits{
					HttpRequests: 1000,
					Bandwidth:    872,
				},
			},
		)

		require.NoError(t, err)

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

		data, err := zstdDecompress(compressedData)
		require.NoError(t, err)

		var lls pb.LabelLinks

		err = lls.Unmarshal(data)
		require.NoError(t, err)

		require.Equal(t, 1, len(lls.LabelLinks))

		ll := lls.LabelLinks[0]

		assert.Equal(t, accountId, ll.Account.AccountId)
		assert.Equal(t, label, ll.Labels)
		assert.Equal(t, target, ll.Target)

		require.NotNil(t, ll.Limits)

		assert.Equal(t, float64(1000), ll.Limits.HttpRequests)
		assert.Equal(t, float64(872), ll.Limits.Bandwidth)

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

		data, err = zstdDecompress(compressedData)
		require.NoError(t, err)

		var lls2 pb.LabelLinks

		err = lls2.Unmarshal(data)
		require.NoError(t, err)

		require.Equal(t, 0, len(lls2.LabelLinks))
	})

	t.Run("can create and remove a service for an account", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "hzn")
		defer db.Close()

		var s Server
		s.L = L
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"
		s.awsSess = sess
		s.bucket = bucket
		s.lockMgr = &inmemLockMgr{}

		s.m, _ = metrics.New(metrics.DefaultConfig("test"), &metrics.BlackholeSink{})

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
		account := &pb.Account{
			Namespace: "/",
			AccountId: accountId,
		}

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

		// Check the account payload written to s3

		s3api := s3.New(sess)

		resp, err := s3api.GetObject(&s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String("account_services/" + account.HashKey()),
		})

		require.NoError(t, err)

		compressedData, err := ioutil.ReadAll(resp.Body)
		require.NoError(t, err)

		data, err := zstdDecompress(compressedData)
		require.NoError(t, err)

		var accs pb.AccountServices

		err = accs.Unmarshal(data)
		require.NoError(t, err)

		require.Equal(t, 1, len(accs.Services))

		sr := accs.Services[0]

		assert.Equal(t, hubId, sr.Hub)
		assert.Equal(t, serviceId, sr.Id)
		assert.Equal(t, labels, sr.Labels)
		assert.Equal(t, "test", sr.Type)

		{
			// Verify we have the service
			resp, err := s.ListServices(
				metadata.NewIncomingContext(top, md3),
				&pb.ListServicesRequest{
					Account: account,
				},
			)
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.Len(t, resp.Services, 1)
			require.Equal(t, resp.Services[0].Id, serviceId)
		}

		_, err = s.RemoveService(
			metadata.NewIncomingContext(top, md3),
			&pb.ServiceRequest{
				Account: account,
				Hub:     hubId,
				Id:      serviceId,
			},
		)
		require.NoError(t, err)

		var so Service
		err = dbx.Check(db.First(&so))

		assert.Error(t, err)

		resp, err = s3api.GetObject(&s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String("account_services/" + account.HashKey()),
		})

		require.NoError(t, err)

		compressedData, err = ioutil.ReadAll(resp.Body)
		require.NoError(t, err)

		data, err = zstdDecompress(compressedData)
		require.NoError(t, err)

		var accs2 pb.AccountServices

		err = accs2.Unmarshal(data)
		require.NoError(t, err)

		require.Equal(t, 0, len(accs2.Services))
	})

	t.Run("supports using consul for account locking", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "hzn")
		defer db.Close()

		top, cancel := context.WithCancel(context.Background())
		defer cancel()

		var s Server
		s.L = L
		s.db = db
		s.vaultClient = vc
		s.vaultPath = pb.NewULID().SpecString()
		s.keyId = "k1"
		s.registerToken = "aabbcc"
		s.awsSess = sess
		s.bucket = bucket

		s.m, _ = metrics.New(metrics.DefaultConfig("test"), &metrics.BlackholeSink{})

		lm, err := NewConsulLockManager(top)
		require.NoError(t, err)
		s.lockMgr = lm

		pub, err := token.SetupVault(vc, s.vaultPath)
		require.NoError(t, err)

		s.pubKey = pub

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

		// Check the account payload written to s3

		s3api := s3.New(sess)

		resp, err := s3api.GetObject(&s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String("account_services/" + account.HashKey()),
		})

		require.NoError(t, err)

		compressedData, err := ioutil.ReadAll(resp.Body)
		require.NoError(t, err)

		data, err := zstdDecompress(compressedData)
		require.NoError(t, err)

		var accs pb.AccountServices

		err = accs.Unmarshal(data)
		require.NoError(t, err)

		require.Equal(t, 1, len(accs.Services))

		sr := accs.Services[0]

		assert.Equal(t, hubId, sr.Hub)
		assert.Equal(t, serviceId, sr.Id)
		assert.Equal(t, labels, sr.Labels)
		assert.Equal(t, "test", sr.Type)

		{
			// Verify we have the service
			resp, err := s.ListServices(
				metadata.NewIncomingContext(top, md3),
				&pb.ListServicesRequest{
					Account: account,
				},
			)
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.Len(t, resp.Services, 1)
			require.Equal(t, resp.Services[0].Id, serviceId)
		}

		// We're going to take the lock so that RemoveService blocks

		accountKey := account.HashKey()

		lockKey := "account-" + accountKey

		accountLock, err := s.lockMgr.GetLock(lockKey, "old")
		require.NoError(t, err)

		ts := time.Now()

		go func() {
			time.Sleep(5 * time.Second)
			accountLock.Close()
		}()

		_, err = s.RemoveService(
			metadata.NewIncomingContext(top, md3),
			&pb.ServiceRequest{
				Account: account,
				Hub:     hubId,
				Id:      serviceId,
			},
		)
		require.NoError(t, err)

		assert.InDelta(t, 5*time.Second, time.Since(ts), float64(time.Second/10))

		var so Service
		err = dbx.Check(db.First(&so))

		assert.Error(t, err)

		resp, err = s3api.GetObject(&s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String("account_services/" + account.HashKey()),
		})

		require.NoError(t, err)

		compressedData, err = ioutil.ReadAll(resp.Body)
		require.NoError(t, err)

		data, err = zstdDecompress(compressedData)
		require.NoError(t, err)

		var accs2 pb.AccountServices

		err = accs2.Unmarshal(data)
		require.NoError(t, err)

		require.Equal(t, 0, len(accs2.Services))

		// Now lets simulate another node using a consul lock manager and seeing it lock

		on, err := NewConsulLockManager(ctx)
		require.NoError(t, err)
		accountLock2, err := on.GetLock(lockKey, "old")
		require.NoError(t, err)

		ts = time.Now()

		go func() {
			time.Sleep(5 * time.Second)
			accountLock2.Close()
		}()

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

		// this is 6 rather than 5 because consulLockMgr uses a 1 second LockWaitTime as well
		assert.InDelta(t, 6*time.Second, time.Since(ts), float64(time.Second))
	})

}

package control

import (
	context "context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	io "io"
	"io/ioutil"
	"math/big"
	mrand "math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/net/http2"
	client "k8s.io/client-go/kubernetes"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/hashicorp/go-hclog"
	lru "github.com/hashicorp/golang-lru"
	"github.com/hashicorp/horizon/pkg/grpc/lz4"
	grpctoken "github.com/hashicorp/horizon/pkg/grpc/token"
	"github.com/hashicorp/horizon/pkg/netloc"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/periodic"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	gcreds "google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type Peer struct {
	PublicKey []byte
}

var accountIdleExpire = 2 * time.Hour

type accountInfo struct {
	Mu       sync.RWMutex
	MapKey   string
	LastUse  time.Time
	S3Key    string
	FileName string
	Services *pb.AccountServices
	Process  chan struct{}
	LastMD5  string

	// Populated by pushes from the server
	Recent []*pb.ServiceRoute
}

type mode int

const (
	unknownMode mode = iota
	edgeMode
	s3Mode
)

type Client struct {
	L hclog.Logger

	cfg ClientConfig

	instanceId *pb.ULID

	cancel func()

	mu    sync.RWMutex
	peers map[string]*Peer

	accountServices map[string]*accountInfo

	bucket string
	s3api  *s3.S3

	workDir string

	client pb.ControlServicesClient
	gcc    *grpc.ClientConn

	localServices map[string]*pb.ServiceRequest

	labelMu              sync.RWMutex
	lastLabelMD5         string
	labelLinks           *pb.LabelLinks
	recentLabelLinks     []*pb.LabelLink
	lessRecentLabelLinks []*pb.LabelLink

	rawtlsCert []byte
	rawtlsKey  []byte
	tlsCert    *tls.Certificate
	tokenPub   ed25519.PublicKey

	hubActivity chan *pb.HubActivity

	netloc []*pb.NetworkLocation

	clientset *client.Clientset

	liveHubs *lru.ARCCache

	// mode indicates the operational mode of the client. This is based
	// on the configuration sent by the control server when bootstraping
	mode mode

	edge     pb.EdgeServicesClient
	edgeData clientEdgeData
}

type hubLiveness struct {
	alive     bool
	retiredAt time.Time
}

type ClientConfig struct {
	Logger     hclog.Logger
	InstanceId *pb.ULID
	Id         *pb.ULID
	GRPCConn   *grpc.ClientConn
	Client     pb.ControlServicesClient
	Token      string
	Addr       string
	Version    string
	S3Bucket   string
	Session    *session.Session
	WorkDir    string
	Insecure   bool

	// The kubernetes deployment name used for the service using this client
	K8Deployment string

	// Where hub integrates it's handler for the hzn protocol
	NextProto map[string]func(hs *http.Server, tlsConn *tls.Conn, h http.Handler)

	FilterRoute        func(*pb.ServiceRoute) bool
	InsecureSkipVerify bool
}

func NewClient(ctx context.Context, cfg ClientConfig) (*Client, error) {
	if cfg.Logger == nil {
		cfg.Logger = hclog.L()
	}

	var (
		gcc  *grpc.ClientConn
		edge pb.EdgeServicesClient
		err  error
	)

	gClient := cfg.Client

	if gClient == nil && cfg.GRPCConn != nil {
		gClient = pb.NewControlServicesClient(cfg.GRPCConn)
		edge = pb.NewEdgeServicesClient(cfg.GRPCConn)
	}

	if gClient == nil && cfg.Addr != "" {
		opts := []grpc.DialOption{
			grpc.WithPerRPCCredentials(grpctoken.Token(cfg.Token)),
			grpc.WithDefaultCallOptions(grpc.UseCompressor(lz4.Name)),
		}

		if cfg.Insecure {
			opts = append(opts, grpc.WithInsecure())
		} else {
			creds := gcreds.NewTLS(&tls.Config{
				InsecureSkipVerify: cfg.InsecureSkipVerify,
			})

			opts = append(opts, grpc.WithTransportCredentials(creds))
		}

		gcc, err = grpc.Dial(cfg.Addr, opts...)
		if err != nil {
			return nil, err
		}

		gClient = pb.NewControlServicesClient(gcc)
		edge = pb.NewEdgeServicesClient(gcc)
	}

	liveHubs, err := lru.NewARC(1000)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)

	instanceId := cfg.InstanceId
	if instanceId == nil {
		instanceId = pb.NewULID()
	}

	var m mode

	if cfg.Session != nil {
		m = s3Mode
	} else if edge != nil {
		m = edgeMode
	}

	client := &Client{
		L:               cfg.Logger,
		cfg:             cfg,
		instanceId:      instanceId,
		client:          gClient,
		edge:            edge,
		gcc:             gcc,
		accountServices: make(map[string]*accountInfo),
		localServices:   make(map[string]*pb.ServiceRequest),
		workDir:         cfg.WorkDir,
		bucket:          cfg.S3Bucket,
		cancel:          cancel,
		hubActivity:     make(chan *pb.HubActivity, 10),
		liveHubs:        liveHubs,
		mode:            m,
	}

	if cfg.Session != nil {
		client.s3api = s3.New(cfg.Session)
	}

	client.edgeData.init()

	return client, nil
}

func (c *Client) Close(ctx context.Context) error {
	// If this errors out, c'est la vie.

	c.client.HubDisconnect(ctx, &pb.HubDisconnectRequest{
		StableId:   c.cfg.Id,
		InstanceId: c.instanceId,
	})

	c.cancel()

	if c.gcc != nil {
		c.gcc.Close()
	}

	return nil
}

func (c *Client) Id() *pb.ULID {
	return c.instanceId
}

func (c *Client) StableId() *pb.ULID {
	return c.cfg.Id
}

func (c *Client) AuthToken() string {
	return c.cfg.Token
}

func (c *Client) SetLocations(netloc []*pb.NetworkLocation) {
	c.netloc = netloc
}

func (c *Client) Locations() []*pb.NetworkLocation {
	return c.netloc
}

func (c *Client) LearnLocations(def *pb.LabelSet) ([]*pb.NetworkLocation, error) {
	locs, err := netloc.Locate(def)
	if err != nil {
		return nil, err
	}

	c.netloc = append(c.netloc, locs...)

	return locs, nil
}

func GenerateSelfSignedTLS() (*tls.Certificate, error) {
	tlspub, tlspriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}

	notBefore := time.Now()

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Horizon Internal"},
		},
		NotBefore: notBefore,
		NotAfter:  notBefore.Add(3650 * (24 * time.Hour)), // 10 years

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"hub.internal"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, tlspub, tlspriv)
	if err != nil {
		return nil, err
	}

	var cert tls.Certificate
	cert.Certificate = [][]byte{derBytes}
	cert.PrivateKey = tlspriv

	return &cert, nil
}

func (c *Client) BootstrapConfig(ctx context.Context) error {
	resp, err := c.client.FetchConfig(ctx, &pb.ConfigRequest{
		StableId:   c.StableId(),
		InstanceId: c.instanceId,
		Locations:  c.netloc,
	})
	if err != nil {
		return err
	}

	c.rawtlsCert = resp.TlsCert
	c.rawtlsKey = resp.TlsKey
	c.tokenPub = resp.TokenPub

	if c.rawtlsKey == nil {
		cert, err := GenerateSelfSignedTLS()
		if err != nil {
			return err
		}

		c.tlsCert = cert
	} else {
		cert, err := tls.X509KeyPair(c.rawtlsCert, c.rawtlsKey)
		if err != nil {
			return err
		}

		c.tlsCert = &cert
	}

	if resp.S3AccessKey != "" {
		c.mode = s3Mode

		L := c.L

		L.Info("reconfiguring s3 access to use server provided credentials",
			"bucket", resp.S3Bucket,
			"access-key", resp.S3AccessKey,
			"token-pub", hex.EncodeToString(c.tokenPub),
		)

		var cfg aws.Config

		cfg.WithCredentials(credentials.NewStaticCredentials(resp.S3AccessKey, resp.S3SecretKey, ""))

		c.cfg.Session = session.New(&cfg)
		c.s3api = s3.New(c.cfg.Session)

		c.bucket = resp.S3Bucket
		c.cfg.S3Bucket = resp.S3Bucket
	} else if c.s3api != nil {
		c.L.Info("detecting existing s3 configure, using s3 mode")
		c.mode = s3Mode
	} else {
		c.L.Info("no s3 access configured, using edge mode")
		// In edge mode, we just make requests to the control server for all our needs.
		c.mode = edgeMode
	}

	if resp.ImageTag != "" {
		c.checkImageTag(ctx, resp.ImageTag, true)
	}

	return nil
}

func (c *Client) TokenPub() ed25519.PublicKey {
	return c.tokenPub
}

type NPNHandler func(hs *http.Server, c *tls.Conn, h http.Handler)

func (c *Client) RunIngress(ctx context.Context, li net.Listener, npn map[string]NPNHandler, h http.Handler) error {
	L := c.L

	var cfg tls.Config

	if c.tlsCert == nil {
		cert, err := tls.X509KeyPair(c.rawtlsCert, c.rawtlsKey)
		if err != nil {
			return err
		}

		c.tlsCert = &cert
	}

	cfg.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		return c.tlsCert, nil
	}

	hs := &http.Server{
		Handler:   h,
		TLSConfig: &cfg,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}

	conf := &http2.Server{
		NewWriteScheduler: func() http2.WriteScheduler { return http2.NewPriorityWriteScheduler(nil) },
	}

	http2.ConfigureServer(hs, conf)

	for proto, fn := range c.cfg.NextProto {
		hs.TLSConfig.NextProtos = append(hs.TLSConfig.NextProtos, proto)
		hs.TLSNextProto[proto] = fn
	}

	for proto, fn := range npn {
		hs.TLSConfig.NextProtos = append(hs.TLSConfig.NextProtos, proto)
		hs.TLSNextProto[proto] = fn
	}

	go func() {
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		hs.Shutdown(ctx)
	}()

	period := time.Hour

	L.Info("refreshing bootstrap config", "period", period)

	go periodic.Run(ctx, period, func() {
		L.Info("periodic rebootstraping of hub config")
		err := c.BootstrapConfig(ctx)
		if err != nil {
			L.Error("error bootstraping new configuration", "error", err)
		}
	})

	L.Info("client ingress running")

	return hs.ServeTLS(li, "", "")
}

func (c *Client) NumLocalServices() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.localServices)
}

func (c *Client) AddService(ctx context.Context, serv *pb.ServiceRequest) error {
	serv.Hub = c.instanceId
	_, err := c.client.AddService(ctx, serv)
	if err != nil {
		return err
	}

	// quick dup
	data, err := serv.Marshal()
	if err != nil {
		return err
	}

	var dup pb.ServiceRequest
	err = dup.Unmarshal(data)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.localServices[dup.Id.SpecString()] = &dup

	return err
}

func (c *Client) RemoveService(ctx context.Context, serv *pb.ServiceRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.localServices, serv.Id.SpecString())

	_, err := c.client.RemoveService(ctx, serv)
	return err
}

const deploymentOrder = ":deployment-order"

type RouteCalculation struct {
	instanceId *pb.ULID

	All  []*pb.ServiceRoute
	Best []*pb.ServiceRoute
}

func (c *RouteCalculation) Empty() bool {
	return len(c.Best) == 0 && len(c.All) == 0
}

func (c *RouteCalculation) shuffle(in []*pb.ServiceRoute) []*pb.ServiceRoute {
	if len(in) < 2 {
		return in
	}

	var (
		local  []*pb.ServiceRoute
		remote []*pb.ServiceRoute
	)

	for _, r := range in {
		if r.Hub.Equal(c.instanceId) {
			local = append(local, r)
		} else {
			remote = append(remote, r)
		}
	}

	mrand.Shuffle(len(local), func(i, j int) {
		local[i], local[j] = local[j], local[i]
	})

	mrand.Shuffle(len(remote), func(i, j int) {
		remote[i], remote[j] = remote[j], remote[i]
	})

	return append(local, remote...)
}

func (c *RouteCalculation) FindBest() {
	best := make([]*pb.ServiceRoute, len(c.All))
	copy(best, c.All)

	depOrder := make([]string, len(best))

	for i, r := range best {
		lbl, _ := r.Labels.GetLabel(deploymentOrder)
		depOrder[i] = lbl
	}

	sort.Slice(best, func(i, j int) bool {
		o1 := depOrder[i]
		o2 := depOrder[j]

		if o1 == "" {
			return true
		} else if o2 == "" {
			return false
		}

		return o1 < o2
	})

	c.Best = best
}

func (c *RouteCalculation) Services() []*pb.ServiceRoute {
	if len(c.Best) > 0 {
		return c.shuffle(c.Best)
	}

	return c.shuffle(c.All)
}

func (c *Client) LookupService(ctx context.Context, account *pb.Account, labels *pb.LabelSet) (*RouteCalculation, error) {
	switch c.mode {
	case edgeMode:
		return c.lookupServiceEdge(ctx, account, labels)
	case s3Mode:
		// ok, handled below.
	default:
		return nil, errors.Wrapf(ErrInvalidRequest, "unknown mode configured: %d", c.mode)
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	var (
		out       []*pb.ServiceRoute
		best      []*pb.ServiceRoute
		bestOrder string
		rest      []*pb.ServiceRoute
	)

	maintainBest := func(route *pb.ServiceRoute) {
		order, ok := route.Labels.GetLabel(deploymentOrder)
		if ok {
			if best != nil {
				if order > bestOrder {
					best = []*pb.ServiceRoute{route}
					bestOrder = order
				} else if order == bestOrder {
					best = append(best, route)
				}
			} else {
				best = []*pb.ServiceRoute{route}
				bestOrder = order
			}
		} else if best != nil {
			rest = append(rest, route)
		}
	}

	for _, reg := range c.localServices {
		if reg.Account.Equal(account) && labels.Matches(reg.Labels) {
			route := &pb.ServiceRoute{
				Id:     reg.Id,
				Hub:    reg.Hub,
				Type:   reg.Type,
				Labels: reg.Labels,
			}

			out = append(out, route)

			maintainBest(route)
		}
	}

	accStr := account.StringKey()

	info, ok := c.accountServices[accStr]
	if !ok {
		info = &accountInfo{
			MapKey:   accStr,
			S3Key:    "account_services/" + account.HashKey(),
			LastUse:  time.Now(),
			FileName: account.HashKey(),
			Process:  make(chan struct{}),
		}

		c.accountServices[accStr] = info

		c.refreshAcconut(c.L, info)
	}

	for _, service := range info.Recent {
		// Skip yourself, you already got those.
		if service.Hub.Equal(c.instanceId) {
			continue
		}

		// If this is for a hub we know is not alive, skip it.
		if val, ok := c.liveHubs.Get(service.Hub.SpecString()); ok && !val.(*hubLiveness).alive {
			continue
		}

		// If the user has requested the ability to filter the services also then give
		// them the chance here.
		if c.cfg.FilterRoute != nil {
			if !c.cfg.FilterRoute(service) {
				continue
			}
		}

		if labels.Matches(service.Labels) {
			out = append(out, service)
			maintainBest(service)
		}
	}

	if info.Services != nil {
		for _, service := range info.Services.Services {
			// Skip yourself, you already got those.
			if service.Hub.Equal(c.instanceId) {
				continue
			}

			// If this is for a hub we know is not alive, skip it.
			if val, ok := c.liveHubs.Get(service.Hub.SpecString()); ok && !val.(*hubLiveness).alive {
				continue
			}

			// If the user has requested the ability to filter the services also then give
			// them the chance here.
			if c.cfg.FilterRoute != nil {
				if !c.cfg.FilterRoute(service) {
					continue
				}
			}

			if labels.Matches(service.Labels) {
				out = append(out, service)
				maintainBest(service)
			}
		}
	}

	ret := &RouteCalculation{
		instanceId: c.instanceId,
		All:        out,
	}

	if len(best) > 0 {
		ret.Best = append(best, rest...)
	}

	return ret, nil
}

func (c *Client) refreshAcconut(L hclog.Logger, info *accountInfo) {
	tmp, err := ioutil.TempFile(c.workDir, info.FileName)
	if err != nil {
		L.Error("error creating temp file for account data", "error", err)
		return
	}

	defer os.Remove(tmp.Name())

	obj := &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    &info.S3Key,
	}

	if info.LastMD5 != "" {
		obj.IfNoneMatch = aws.String(info.LastMD5)
	}

	resp, err := c.s3api.GetObject(obj)
	if err != nil {
		if rf, ok := err.(awserr.RequestFailure); ok {
			if rf.StatusCode() == 304 {
				L.Trace("account data not modified", "key", info.S3Key)
				return
			}

			if rf.StatusCode() == 404 {
				L.Trace("no account data available", "key", info.S3Key)
				return
			}
		}
		L.Error("error fetching account data", "error", err, "key", info.S3Key, "bucket", c.bucket)
		return
	}

	defer resp.Body.Close()

	n, err := io.Copy(tmp, resp.Body)
	if err != nil {
		L.Error("error copying s3 object to disk", "error", err)
		return
	}

	L.Debug("downloaded account data", "key", info.S3Key, "size", n)

	err = os.Rename(tmp.Name(), filepath.Join(c.workDir, info.FileName))
	if err != nil {
		L.Error("error renaming account data", "tmpfile", tmp.Name(), "target", info.FileName)
		return
	}

	_, err = tmp.Seek(0, os.SEEK_SET)
	if err != nil {
		L.Error("error seeking account data to start of file", "error", err)
		return
	}

	compressedData, err := ioutil.ReadAll(tmp)
	if err != nil {
		L.Error("error reading account data", "error", err)
		return
	}

	data, err := zstdDecompress(compressedData)
	if err != nil {
		L.Error("error uncompressing data", "error", err)
		return
	}

	var ac pb.AccountServices
	err = ac.Unmarshal(data)
	if err != nil {
		L.Error("error unmarshaling account services", "error", err)
		return
	}

	info.LastMD5 = *resp.ETag

	info.Mu.Lock()
	defer info.Mu.Unlock()

	info.Services = &ac
}

func (c *Client) checkAccounts(L hclog.Logger) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	threshold := time.Now().Add(-time.Minute)

	for _, info := range c.accountServices {
		if info.LastUse.Before(threshold) {
			go c.refreshAcconut(L, info)
		}
	}
}

func (c *Client) streamActivity(
	ctx context.Context, L hclog.Logger, ch chan *pb.CentralActivity,
) (
	pb.ControlServices_StreamActivityClient, error,
) {
	activity, err := c.client.StreamActivity(ctx)
	if err != nil {
		return nil, err
	}

	err = activity.Send(&pb.HubActivity{
		HubReg: &pb.HubActivity_HubRegistration{
			Hub:       c.instanceId,
			StableHub: c.cfg.Id,
			Locations: c.netloc,
		},
		SentAt: pb.NewTimestamp(time.Now()),
	})

	if err != nil {
		return nil, err
	}

	L.Info("waiting on server activity")

	go func() {
		defer close(ch)

		for {
			ca, err := activity.Recv()
			if err != nil {
				if status, ok := status.FromError(err); ok && status.Code() == codes.Canceled {
					return
				}

				L.Error("error reading activity", "error", err)
				return
			}

			L.Debug("received acvitity from control", "activity", ca)

			select {
			case <-ctx.Done():
				return
			case ch <- ca:
				// ok
			}
		}
	}()

	return activity, nil
}

func (c *Client) Run(ctx context.Context) error {
	L := c.L

	err := c.updateLabelLinks(ctx, L)
	if err != nil {
		return err
	}

	var activity pb.ControlServices_StreamActivityClient

	activityChan := make(chan *pb.CentralActivity)

	if c.client != nil {
		L.Debug("configuring activity stream")
		activity, err = c.streamActivity(ctx, L, activityChan)
		if err != nil {
			return err
		}

		defer activity.CloseSend()
	} else {
		L.Debug("no client present, activity stream disabled")
	}

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.checkAccounts(L)
			err := c.updateLabelLinks(ctx, L)
			if err != nil {
				L.Error("error updating label links", "error", err)
			}
		case ev, ok := <-activityChan:
			if !ok {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}

				L.Error("detected activity stream closed, reconnecting...")
				activityChan = make(chan *pb.CentralActivity)
				for {
					activity, err = c.streamActivity(ctx, L, activityChan)
					if err == nil {
						break
					}
				}
				L.Info("rebootstraping after activity stream reconnection")
				err = c.BootstrapConfig(ctx)
				if err != nil {
					L.Error("error bootstraping new configuration", "error", err)
				}
			} else {
				c.processCentralActivity(ctx, L, ev)
			}
		case act := <-c.hubActivity:
			if activity != nil {
				activity.Send(act)
			}
		}
	}
}

func (c *Client) processCentralActivity(ctx context.Context, L hclog.Logger, ev *pb.CentralActivity) {
	L.Debug("processing activity from central")

	for _, acc := range ev.AccountServices {
		u := acc.Account.StringKey()

		c.mu.RLock()
		info, ok := c.accountServices[u]
		if ok {
			info.LastUse = time.Now()
		}
		c.mu.RUnlock()

		// We weren't tracking this account, bail
		if !ok {
			continue
		}

		info.Recent = append(info.Recent, acc.Services...)
	}

	if ev.NewLabelLinks != nil {
		L.Debug("updating recent label links")
		c.recentLabelLinks = append(c.recentLabelLinks, ev.NewLabelLinks.LabelLinks...)
	}

	if ev.HubChange != nil {
		L.Debug("updating live hubs")
		c.mu.Lock()

		if ev.HubChange.OldId != nil {
			key := ev.HubChange.OldId.SpecString()

			val, ok := c.liveHubs.Get(key)

			if !ok {
				c.liveHubs.Add(key, &hubLiveness{
					alive:     false,
					retiredAt: time.Now(),
				})
			} else {
				oldLv := val.(*hubLiveness)
				oldLv.alive = false
				oldLv.retiredAt = time.Now()
			}
		}

		if ev.HubChange.NewId != nil {
			key := ev.HubChange.NewId.SpecString()

			val, ok := c.liveHubs.Get(key)

			if !ok {
				c.liveHubs.Add(key, &hubLiveness{
					alive: true,
				})
			} else {
				oldLv := val.(*hubLiveness)
				oldLv.alive = true
				oldLv.retiredAt = time.Time{}
			}
		}

		c.mu.Unlock()
	}
}

func (c *Client) SendFlow(rec *pb.FlowRecord) {
	c.hubActivity <- &pb.HubActivity{
		Flow: []*pb.FlowRecord{rec},
	}
}

func (c *Client) ForceLabelLinkUpdate(ctx context.Context, L hclog.Logger) error {
	return c.updateLabelLinks(ctx, L)
}

func (c *Client) updateLabelLinks(ctx context.Context, L hclog.Logger) error {
	if c.bucket == "" {
		L.Debug("no bucket configured, not updating label links")
		return nil
	}

	L.Trace("updating label links")

	c.labelMu.Lock()
	c.lessRecentLabelLinks = c.recentLabelLinks
	c.recentLabelLinks = nil
	c.labelMu.Unlock()

	tmp, err := ioutil.TempFile(c.workDir, "label-links")
	if err != nil {
		return err
	}

	defer os.Remove(tmp.Name())

	obj := &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String("label_links"),
	}

	if c.lastLabelMD5 != "" {
		obj.IfNoneMatch = aws.String(c.lastLabelMD5)
	}

	resp, err := c.s3api.GetObjectWithContext(ctx, obj)
	if err != nil {
		if rf, ok := err.(awserr.RequestFailure); ok {
			if rf.StatusCode() == 304 {
				L.Trace("label links not modified")
				return nil
			}

			if rf.StatusCode() == 404 {
				L.Trace("no label links available")
				return nil
			}
		}

		if s3e, ok := err.(awserr.Error); ok {
			if s3e.Code() == s3.ErrCodeNoSuchKey {
				return nil
			}
		}

		return err
	}

	defer resp.Body.Close()

	n, err := io.Copy(tmp, resp.Body)
	if err != nil {
		return err
	}

	L.Debug("downloaded label links data", "size", n)

	err = os.Rename(tmp.Name(), filepath.Join(c.workDir, "label-links"))
	if err != nil {
		return err
	}

	_, err = tmp.Seek(0, os.SEEK_SET)
	if err != nil {
		return err
	}

	compressedData, err := ioutil.ReadAll(tmp)
	if err != nil {
		return err
	}

	data, err := zstdDecompress(compressedData)
	if err != nil {
		return err
	}

	var lls pb.LabelLinks
	err = lls.Unmarshal(data)
	if err != nil {
		return err
	}

	c.lastLabelMD5 = *resp.ETag

	c.labelMu.Lock()
	defer c.labelMu.Unlock()

	c.labelLinks = &lls

	L.Info("label links updated", "etag", c.lastLabelMD5, "size", len(c.labelLinks.LabelLinks))

	return err
}

func (c *Client) ResolveLabelLink(label *pb.LabelSet) (*pb.Account, *pb.LabelSet, *pb.Account_Limits, error) {
	switch c.mode {
	case edgeMode:
		return c.resolveLLEdge(label)
	case s3Mode:
		// ok, handled below.
	default:
		return nil, nil, nil, errors.Wrapf(ErrInvalidRequest, "unknown mode configured: %d", c.mode)
	}

	c.labelMu.RLock()
	defer c.labelMu.RUnlock()

	label.Finalize()

	mature := 0

	if c.labelLinks != nil {
		mature = len(c.labelLinks.LabelLinks)
	}

	c.L.Debug("label-links to consider",
		"recent", len(c.recentLabelLinks),
		"less-recent", len(c.lessRecentLabelLinks),
		"mature", mature,
	)

	for _, ll := range c.recentLabelLinks {
		if ll.Labels.Equal(label) {
			return ll.Account, ll.Target, ll.Limits, nil
		}
	}

	// We move the recent to lessRecent when we update all the label links.
	// This 2 layer technique means we have no gaps where we might miss an
	// immediate update.
	for _, ll := range c.lessRecentLabelLinks {
		if ll.Labels.Equal(label) {
			return ll.Account, ll.Target, ll.Limits, nil
		}
	}

	if c.labelLinks == nil {
		return nil, nil, nil, nil
	}

	for _, ll := range c.labelLinks.LabelLinks {
		if ll.Labels.Equal(label) {
			return ll.Account, ll.Target, ll.Limits, nil
		}
	}

	return nil, nil, nil, nil
}

func (c *Client) AllHubs(ctx context.Context) ([]*pb.HubInfo, error) {
	list, err := c.client.AllHubs(ctx, &pb.Noop{})
	if err != nil {
		return nil, err
	}

	return list.Hubs, nil
}

func (c *Client) GetHubAddresses(ctx context.Context, id *pb.ULID) ([]*pb.NetworkLocation, error) {
	list, err := c.client.AllHubs(ctx, &pb.Noop{})
	if err != nil {
		return nil, err
	}

	for _, hub := range list.Hubs {
		if hub.Id.Equal(id) {
			return hub.Locations, nil
		}
	}

	return nil, nil
}

func (c *Client) RequestServiceToken(ctx context.Context, namespace string) (string, error) {
	resp, err := c.client.RequestServiceToken(ctx, &pb.ServiceTokenRequest{
		Namespace: namespace,
	})

	if err != nil {
		return "", err
	}

	return resp.Token, nil
}

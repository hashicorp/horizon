package control

import (
	context "context"
	"crypto/ed25519"
	"crypto/tls"
	io "io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/net/http2"

	"github.com/DataDog/zstd"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/ctxlog"
	"github.com/hashicorp/horizon/pkg/pb"
	"google.golang.org/grpc"
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

type Client struct {
	L hclog.Logger

	cfg ClientConfig

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

	labelMu      sync.RWMutex
	lastLabelMD5 string
	labelLinks   *pb.LabelLinks

	tlsCert  []byte
	tlsKey   []byte
	tokenPub ed25519.PublicKey

	hubActivity chan *pb.HubActivity
}

type ClientConfig struct {
	Logger   hclog.Logger
	Id       *pb.ULID
	Client   pb.ControlServicesClient
	Token    string
	Addr     string
	Version  string
	S3Bucket string
	Session  *session.Session
	WorkDir  string

	// Where hub integrates it's handler for the hzn protocol
	NextProto map[string]func(hs *http.Server, tlsConn *tls.Conn, h http.Handler)
}

func NewClient(ctx context.Context, cfg ClientConfig) (*Client, error) {
	if cfg.Logger == nil {
		cfg.Logger = hclog.L()
	}

	var (
		gcc *grpc.ClientConn
		err error
	)

	gClient := cfg.Client
	if gClient == nil && cfg.Addr != "" {
		gcc, err = grpc.Dial(cfg.Addr, grpc.WithPerRPCCredentials(Token(cfg.Token)))
		if err != nil {
			return nil, err
		}

		gClient = pb.NewControlServicesClient(gcc)
	}

	ctx, cancel := context.WithCancel(ctx)

	client := &Client{
		L:               cfg.Logger,
		cfg:             cfg,
		client:          gClient,
		gcc:             gcc,
		accountServices: make(map[string]*accountInfo),
		localServices:   make(map[string]*pb.ServiceRequest),
		s3api:           s3.New(cfg.Session),
		workDir:         cfg.WorkDir,
		bucket:          cfg.S3Bucket,
		cancel:          cancel,
		hubActivity:     make(chan *pb.HubActivity, 10),
	}

	// go client.keepLabelLinksUpdated(ctx, cfg.Logger)

	return client, nil
}

func (c *Client) Close() error {
	c.cancel()

	if c.gcc != nil {
		c.gcc.Close()
	}

	return nil
}

func (c *Client) Id() *pb.ULID {
	return c.cfg.Id
}

func (c *Client) BootstrapConfig(ctx context.Context) error {
	resp, err := c.client.FetchConfig(ctx, &pb.ConfigRequest{
		Hub: c.cfg.Id,
	})
	if err != nil {
		return err
	}

	c.tlsCert = resp.TlsCert
	c.tlsKey = resp.TlsKey
	c.tokenPub = resp.TokenPub

	return nil
}

func (c *Client) TokenPub() ed25519.PublicKey {
	return c.tokenPub
}

type NPNHandler func(hs *http.Server, c *tls.Conn, h http.Handler)

func (c *Client) RunIngress(ctx context.Context, li net.Listener, npn map[string]NPNHandler) error {
	L := ctxlog.L(ctx)

	var cfg tls.Config
	cfg.Certificates = []tls.Certificate{
		{
			Certificate: [][]byte{c.tlsCert},
			PrivateKey:  ed25519.PrivateKey(c.tlsKey),
		},
	}

	hs := &http.Server{
		Handler:   c,
		TLSConfig: &cfg,
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

	L.Info("client ingress running")

	return hs.ServeTLS(li, "", "")
}

func (c *Client) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(200)
}

func (c *Client) AddService(ctx context.Context, serv *pb.ServiceRequest) error {
	serv.Hub = c.cfg.Id
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

func (c *Client) LookupService(ctx context.Context, accountId *pb.ULID, labels *pb.LabelSet) ([]*pb.ServiceRoute, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var out []*pb.ServiceRoute

	for _, reg := range c.localServices {
		if labels.Matches(reg.Labels) {
			out = append(out, &pb.ServiceRoute{
				Id:     reg.Id,
				Hub:    reg.Hub,
				Type:   reg.Type,
				Labels: reg.Labels,
			})
		}
	}

	accStr := accountId.SpecString()

	info, ok := c.accountServices[accStr]
	if !ok {
		info = &accountInfo{
			MapKey:   accStr,
			S3Key:    "account_services/" + accStr,
			LastUse:  time.Now(),
			FileName: accStr,
			Process:  make(chan struct{}),
		}

		c.accountServices[accStr] = info

		c.refreshAcconut(ctxlog.L(ctx), info)
	}

	for _, service := range info.Recent {
		// Skip yourself, you already got those.
		if service.Hub.Equal(c.cfg.Id) {
			continue
		}

		if labels.Matches(service.Labels) {
			out = append(out, service)
		}
	}

	if info.Services != nil {
		for _, service := range info.Services.Services {
			// Skip yourself, you already got those.
			if service.Hub.Equal(c.cfg.Id) {
				continue
			}

			if labels.Matches(service.Labels) {
				out = append(out, service)
			}
		}
	}

	return out, nil
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
		// Until we can figure out how to detect it, assume that an error here means
		// we got a 304 and the file isn't updated.
		L.Info("error downloading account data - benign due to 304s", "error", err)
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

	data, err := zstd.Decompress(nil, compressedData)
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

func (c *Client) Run(ctx context.Context) error {
	L := ctxlog.L(ctx)

	err := c.updateLabelLinks(ctx, L)
	if err != nil {
		return err
	}

	var activity pb.ControlServices_StreamActivityClient

	activityChan := make(chan *pb.CentralActivity)

	if c.client != nil {
		activity, err = c.client.StreamActivity(ctx)
		if err != nil {
			return err
		}

		err = activity.Send(&pb.HubActivity{
			Hub:    c.cfg.Id,
			SentAt: pb.NewTimestamp(time.Now()),
		})

		if err != nil {
			return err
		}

		L.Info("waiting on server activity")

		defer activity.CloseSend()
		go func() {
			defer close(activityChan)

			for {
				ca, err := activity.Recv()
				if err != nil {
					return
				}

				select {
				case <-ctx.Done():
					return
				case activityChan <- ca:
					// ok
				}
			}
		}()
	}

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.checkAccounts(L)
			c.updateLabelLinks(ctx, L)
		case ev, ok := <-activityChan:
			if !ok {
				break
			}
			c.processCentralActivity(ctx, L, ev)
		case act := <-c.hubActivity:
			activity.Send(act)
		}
	}
}

func (c *Client) processCentralActivity(ctx context.Context, L hclog.Logger, ev *pb.CentralActivity) {
	for _, acc := range ev.AccountServices {
		u := acc.Account.AccountId

		c.mu.RLock()
		info, ok := c.accountServices[u.SpecString()]
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
		return nil
	}

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

	data, err := zstd.Decompress(nil, compressedData)
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

	return err
}

func (c *Client) ResolveLabelLink(label *pb.LabelSet) (*pb.ULID, *pb.LabelSet, error) {
	c.labelMu.RLock()
	defer c.labelMu.RUnlock()

	sort.Sort(label)

	if c.labelLinks == nil {
		return nil, nil, nil
	}

	for _, ll := range c.labelLinks.LabelLinks {
		if ll.Labels.Equal(label) {
			return ll.Account.AccountId, ll.Target, nil
		}
	}

	return nil, nil, nil
}

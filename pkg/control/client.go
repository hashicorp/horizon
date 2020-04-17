package control

import (
	context "context"
	io "io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/DataDog/zstd"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/ctxlog"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/serf/serf"
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
}

type Client struct {
	L hclog.Logger

	cfg ClientConfig

	cancel func()

	events chan serf.Event
	s      *serf.Serf

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
}

type ClientConfig struct {
	Logger   hclog.Logger
	Id       *pb.ULID
	Client   pb.ControlServicesClient
	Token    string
	Addr     string
	Version  string
	BindPort int
	S3Bucket string
	Session  *session.Session
	WorkDir  string
}

func NewClient(ctx context.Context, cfg ClientConfig) (*Client, error) {
	events := make(chan serf.Event, 10)

	if cfg.Logger == nil {
		cfg.Logger = hclog.L()
	}

	scfg := serf.DefaultConfig()
	scfg.NodeName = cfg.Id.SpecString()
	scfg.EventCh = events
	scfg.Tags = map[string]string{
		"version": cfg.Version,
	}

	if cfg.BindPort > 0 {
		scfg.MemberlistConfig.BindPort = cfg.BindPort
	}

	scfg.Logger = cfg.Logger.StandardLogger(&hclog.StandardLoggerOptions{
		InferLevels: true,
	})

	s, err := serf.Create(scfg)
	if err != nil {
		return nil, err
	}

	var gcc *grpc.ClientConn

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
		events:          events,
		s:               s,
		client:          gClient,
		gcc:             gcc,
		accountServices: make(map[string]*accountInfo),
		localServices:   make(map[string]*pb.ServiceRequest),
		s3api:           s3.New(cfg.Session),
		workDir:         cfg.WorkDir,
		bucket:          cfg.S3Bucket,
		cancel:          cancel,
	}

	go client.keepLabelLinksUpdated(ctx, cfg.Logger)

	return client, nil
}

func (c *Client) Close() error {
	c.cancel()

	if c.gcc != nil {
		c.gcc.Close()
	}

	c.s.Leave()

	return c.s.Shutdown()
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

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.checkAccounts(L)
		case ev := <-c.events:
			switch sev := ev.(type) {
			case serf.UserEvent:
				err := c.processUserEvent(L, sev)
				if err != nil {
					L.Error("error processing user event", "error", err)
				}
			}
		}
	}
}

func (c *Client) processUserEvent(L hclog.Logger, ev serf.UserEvent) error {
	switch ev.Name {
	case "account-updated":
		u := pb.ULIDFromBytes(ev.Payload)

		c.mu.RLock()
		info, ok := c.accountServices[u.SpecString()]
		if ok {
			info.LastUse = time.Now()
		}
		c.mu.RUnlock()

		// We weren't tracking this account, bail
		if !ok {
			return nil
		}

		// Kick over the processing goroutine
		go c.refreshAcconut(L, info)
	}

	return nil
}

func (c *Client) keepLabelLinksUpdated(ctx context.Context, L hclog.Logger) {
	err := c.updateLabelLinks(ctx, L)
	if err != nil {
		L.Error("error updating label links", "error", err)
	}

	ticker := time.NewTicker(time.Minute)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := c.updateLabelLinks(ctx, L)
			if err != nil {
				L.Error("error updating label links", "error", err)
			}
		}
	}
}

func (c *Client) updateLabelLinks(ctx context.Context, L hclog.Logger) error {
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
		// Until we can figure out how to detect it, assume that an error here means
		// we got a 304 and the file isn't updated.
		L.Info("error downloading account data - benign due to 304s", "error", err)
		return nil
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

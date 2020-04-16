package control

import (
	context "context"
	"encoding/base64"
	io "io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/ctxlog"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/serf/serf"
	"github.com/oklog/ulid"
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
	events chan serf.Event
	s      *serf.Serf

	mu    sync.RWMutex
	peers map[string]*Peer

	accountServices map[string]*accountInfo

	bucket string
	dl     *s3manager.Downloader
	s3api  *s3.S3

	workDir string
}

func NewClient(id, pubKey []byte) (*Client, error) {
	events := make(chan serf.Event, 10)

	s, err := serf.Create(&serf.Config{
		NodeName: base64.RawURLEncoding.EncodeToString(id),
		EventCh:  events,
		Tags: map[string]string{
			"public-key": base64.StdEncoding.EncodeToString(pubKey),
		},
	})

	if err != nil {
		return nil, err
	}

	client := &Client{
		events: events,
		s:      s,
	}

	return client, nil
}

func (c *Client) idleAccount(info *accountInfo) bool {
	info.Mu.Lock()
	defer info.Mu.Unlock()

	if time.Since(info.LastUse) < accountIdleExpire {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.accountServices, info.MapKey)
	return true
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

	data, err := ioutil.ReadAll(tmp)
	if err != nil {
		L.Error("error reading account data", "error", err)
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

func (c *Client) managerAccount(ctx context.Context, account []byte, info *accountInfo) {
	L := ctxlog.L(ctx)

	timer := time.NewTimer(time.Minute)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if c.idleAccount(info) {
				return
			}

			c.refreshAcconut(L, info)
			timer.Reset(time.Minute)
		case <-info.Process:
			// The docs say we have to do this to be able to reset it properly
			if timer.Stop() {
				<-timer.C
			}

			c.refreshAcconut(L, info)
			timer.Reset(time.Minute)
		}
	}
}

func (c *Client) Run(ctx context.Context) error {
	L := ctxlog.L(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-c.events:
			switch sev := ev.(type) {
			case serf.UserEvent:
				err := c.processUserEvent(sev)
				if err != nil {
					L.Error("error processing user event", "error", err)
				}
			case serf.MemberEvent:
				err := c.processMemberEvent(sev)
				if err != nil {
					L.Error("error procesing member event", "error", err)
				}
			}
		}
	}
}

func (c *Client) processUserEvent(ev serf.UserEvent) error {
	switch ev.Name {
	case "account-updated":
		var u ulid.ULID
		copy(u[:], ev.Payload)

		c.mu.Lock()
		info, ok := c.accountServices[u.String()]
		c.mu.Unlock()

		// We weren't tracking this account, bail
		if !ok {
			return nil
		}

		// Kick over the processing goroutine
		info.Process <- struct{}{}
	}

	return nil
}

func (c *Client) processMemberEvent(ev serf.MemberEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch ev.Type {
	case serf.EventMemberJoin, serf.EventMemberUpdate:
		for _, member := range ev.Members {
			pubkey, err := base64.StdEncoding.DecodeString(member.Tags["public-key"])
			if err == nil {
				c.peers[member.Name] = &Peer{
					PublicKey: pubkey,
				}
			}
		}
	case serf.EventMemberLeave, serf.EventMemberFailed, serf.EventMemberReap:
		for _, member := range ev.Members {
			delete(c.peers, member.Name)
		}
	}

	return nil
}

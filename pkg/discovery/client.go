package discovery

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/netloc"
	"github.com/hashicorp/horizon/pkg/pb"
)

type Client struct {
	URL string

	mu sync.Mutex

	location []*pb.NetworkLocation

	lastData *DiscoveryData

	avaliable []HubConfig
	results   chan *pb.NetworkLocation
}

func NewClient(surl string) (*Client, error) {
	u, err := url.Parse(surl)
	if err != nil {
		surl = fmt.Sprintf("https://%s/%s", surl, HTTPPath)
	} else {
		if u.Scheme == "" {
			u.Scheme = "https"
		}

		if u.Host == "" && u.Path != "" {
			u.Host = u.Path
			u.Path = HTTPPath
		}

		surl = u.String()
	}

	locs, err := netloc.Locate(nil)
	if err != nil {
		return nil, err
	}

	return &Client{
		URL:      surl,
		location: locs,
	}, nil
}

func (c *Client) Refresh(ctx context.Context) error {
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	resp, err := client.Get(c.URL)
	if err != nil {
		return err
	}

	var dd DiscoveryData

	err = json.NewDecoder(resp.Body).Decode(&dd)
	if err != nil {
		return err
	}

	c.lastData = &dd

	ch, err := c.search(ctx)
	if err != nil {
		return err
	}

	c.results = ch

	return nil
}

func (c *Client) search(ctx context.Context) (chan *pb.NetworkLocation, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	input := &netloc.BestInput{
		Local:      c.location,
		Remote:     c.lastData.Hubs,
		PublicOnly: true,
		Latency: func(addr string) error {
			hclog.L().Info("testing latency", "addr", addr)
			resp, err := client.Get(fmt.Sprintf("http://%s/healthz", addr))
			if err == nil {
				resp.Body.Close()
			}
			return err
		},
	}

	ch := make(chan *pb.NetworkLocation, 1)

	go netloc.FindBestLive(ctx, input, ch)

	return ch, nil
}

func (c *Client) Take(ctx context.Context) (HubConfig, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.avaliable) > 0 {
		addr := c.avaliable[0]
		c.avaliable = c.avaliable[1:]
		return addr, true
	}

	select {
	case <-ctx.Done():
		return HubConfig{}, false
	case loc, ok := <-c.results:
		if !ok {
			return HubConfig{}, false
		}

		return HubConfig{
			Addr: loc.Addresses[0] + ":443",
			Name: loc.Name,
		}, true
	}
}

func (c *Client) Return(cfg HubConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.avaliable = append(c.avaliable, cfg)
}

func (c *Client) Best(ctx context.Context, count int) ([]string, error) {
	if c.lastData == nil {
		err := c.Refresh(ctx)
		if err != nil {
			return nil, err
		}
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	locs, err := netloc.FindBest(&netloc.BestInput{
		Count:      count,
		Local:      c.location,
		Remote:     c.lastData.Hubs,
		PublicOnly: true,
		Latency: func(addr string) error {
			hclog.L().Info("testing latency", "addr", addr)
			resp, err := client.Get(fmt.Sprintf("http://%s/healthz", addr))
			if err == nil {
				resp.Body.Close()
			}
			return err
		},
	})

	if err != nil {
		return nil, err
	}

	var addrs []string

	for _, loc := range locs {
		addrs = append(addrs, loc.Addresses[0])
	}

	return addrs, nil
}

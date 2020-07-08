package discovery

import (
	"container/list"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/horizon/pkg/netloc"
	"github.com/hashicorp/horizon/pkg/pb"
)

type Client struct {
	URL string

	mu sync.Mutex

	location []*pb.NetworkLocation

	// A dev hub to always connect to.
	dev string

	lastData *DiscoveryData

	available *list.List
	results   chan *pb.NetworkLocation
	known     map[string]struct{}
	latest    map[string]struct{}
}

func NewClient(surl string) (*Client, error) {
	var dev string

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

		if u.Path == "" {
			u.Path = HTTPPath
		}

		if u.Scheme == "dev" {
			dev = u.Host
		}

		surl = u.String()
	}

	locs, err := netloc.FastLocate(nil)
	if err != nil {
		return nil, err
	}

	return &Client{
		URL:       surl,
		location:  locs,
		dev:       dev,
		available: list.New(),
		known:     make(map[string]struct{}),
		latest:    make(map[string]struct{}),
	}, nil
}

func (c *Client) Refresh(ctx context.Context) error {
	if c.dev != "" {
		c.available.PushFront(HubConfig{
			Addr:     c.dev,
			Insecure: true,
		})
		return nil
	}

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

	go c.backgroundRefresh(ctx)

	return nil
}

func (c *Client) backgroundRefresh(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	var (
		results chan *pb.NetworkLocation
	)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			results, _ = c.search(ctx)
			c.latest = make(map[string]struct{})
		case loc, ok := <-results:
			if !ok {
				results = nil
				continue
			}

			c.mu.Lock()

			c.latest[loc.Name] = struct{}{}

			if _, ok := c.known[loc.Name]; !ok {
				addr := loc.Addresses[0]
				if !strings.Contains(addr, ":") {
					addr += ":443"
				}

				cfg := HubConfig{
					Addr: addr,
					Name: loc.Name,
				}

				c.known[loc.Name] = struct{}{}

				c.available.PushBack(cfg)
			}

			c.mu.Unlock()
		}
	}
}

var ErrBadServer = errors.New("bad server")

func (c *Client) search(ctx context.Context) (chan *pb.NetworkLocation, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	input := &netloc.BestInput{
		Local:      c.location,
		Remote:     c.lastData.Hubs,
		PublicOnly: true,
		Latency: func(addr string) error {
			resp, err := client.Get(fmt.Sprintf("http://%s/__hzn/healthz", addr))
			if err == nil {
				resp.Body.Close()

				if resp.StatusCode != 200 {
					return ErrBadServer
				}
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

	if front := c.available.Front(); front != nil {
		cfg := front.Value.(HubConfig)
		c.available.Remove(front)
		return cfg, true
	}

	select {
	case <-ctx.Done():
		return HubConfig{}, false
	case loc, ok := <-c.results:
		if !ok {
			return HubConfig{}, false
		}

		c.known[loc.Name] = struct{}{}
		c.latest[loc.Name] = struct{}{}

		addr := loc.Addresses[0]
		if !strings.Contains(addr, ":") {
			addr += ":443"
		}

		return HubConfig{
			Addr: addr,
			Name: loc.Name,
		}, true
	}
}

func (c *Client) Return(cfg HubConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// This will automatically prune out broken addresses
	// that the centrial tier is no longer advertising.
	if _, ok := c.latest[cfg.Name]; ok {
		c.available.PushBack(cfg)
	}
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

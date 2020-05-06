package discovery

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/netloc"
	"github.com/hashicorp/horizon/pkg/pb"
)

type Client struct {
	URL string

	location []*pb.NetworkLocation

	lastData *DiscoveryData
}

func NewClient(surl string) (*Client, error) {
	u, err := url.Parse(surl)
	if err != nil {
		surl = fmt.Sprintf("https://%s/%s", surl, HTTPPath)
	} else {
		if u.Scheme == "" {
			u.Scheme = "https"
		}

		spew.Dump(u.Host, u.Path)

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

func (c *Client) Refresh() error {
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

	return nil
}

func (c *Client) Best(count int) ([]string, error) {
	if c.lastData == nil {
		err := c.Refresh()
		if err != nil {
			return nil, err
		}
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	spew.Dump(c.lastData.Hubs)

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

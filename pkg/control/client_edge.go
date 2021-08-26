package control

import (
	context "context"
	"fmt"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"github.com/hashicorp/horizon/pkg/pb"
)

type clientEdgeData struct {
	edgeServiceCache *lru.ARCCache
	edgeLLCache      *lru.ARCCache
}

func (ed *clientEdgeData) init() {
	ed.edgeServiceCache, _ = lru.NewARC(1000)
	ed.edgeLLCache, _ = lru.NewARC(1000)
}

type edgeServiceCacheEntry struct {
	resp      *pb.LookupEndpointsResponse
	rc        *RouteCalculation
	expiresAt time.Time
}

type edgeLLCacheEntry struct {
	resp      *pb.ResolveLabelLinkResponse
	expiresAt time.Time
}

func (c *Client) lookupServiceEdge(ctx context.Context, account *pb.Account, labels *pb.LabelSet) (*RouteCalculation, error) {
	cacheKey := fmt.Sprintf("%s-%s", account.StringKey(), labels.SpecString())

	now := time.Now()

	val, ok := c.edgeData.edgeServiceCache.Get(cacheKey)
	if ok {
		ec := val.(*edgeServiceCacheEntry)
		if ec.expiresAt.After(now) {
			return ec.rc, nil
		}
	}

	resp, err := c.edge.LookupEndpoints(ctx, &pb.LookupEndpointsRequest{
		Account: account,
		Labels:  labels,
	})
	if err != nil {
		return nil, err
	}

	rc := &RouteCalculation{
		instanceId: c.instanceId,
		All:        resp.Routes,
	}
	rc.FindBest()

	if resp.CacheTime > 0 {
		ec := &edgeServiceCacheEntry{
			resp:      resp,
			rc:        rc,
			expiresAt: now.Add(time.Duration(resp.CacheTime) * time.Second),
		}

		c.edgeData.edgeServiceCache.Add(cacheKey, ec)
	}

	return rc, nil
}

func (c *Client) resolveLLEdge(label *pb.LabelSet) (*pb.Account, *pb.LabelSet, *pb.Account_Limits, error) {
	cacheKey := label.SpecString()

	var resp *pb.ResolveLabelLinkResponse

	now := time.Now()

	val, ok := c.edgeData.edgeServiceCache.Get(cacheKey)
	if ok {
		ec := val.(*edgeLLCacheEntry)
		if ec.expiresAt.After(now) {
			resp = ec.resp
		}
	}

	var err error

	if resp == nil {
		resp, err = c.edge.ResolveLabelLink(context.Background(), &pb.ResolveLabelLinkRequest{
			Labels: label,
		})

		if err != nil {
			return nil, nil, nil, err
		}

		if resp.CacheTime > 0 {
			ec := &edgeLLCacheEntry{
				resp:      resp,
				expiresAt: now.Add(time.Duration(resp.CacheTime) * time.Second),
			}

			c.edgeData.edgeLLCache.Add(cacheKey, ec)
		}
	}

	return resp.Account, resp.Labels, resp.Limits, nil
}

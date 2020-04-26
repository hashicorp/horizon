package control

import (
	context "context"
	"sort"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"github.com/hashicorp/horizon/pkg/pb"
	"google.golang.org/grpc/metadata"
)

type FlowTop struct {
	entries *lru.ARCCache
}

const DefaultFlowTopSize = 100

func NewFlowTop(count int) (*FlowTop, error) {
	ent, err := lru.NewARC(count)
	if err != nil {
		return nil, err
	}

	return &FlowTop{
		entries: ent,
	}, nil
}

type FlowTopEntry struct {
	agg     *pb.FlowStream
	updated time.Time
}

func (f *FlowTop) Add(rec *pb.FlowStream) {
	key := rec.FlowId.String()
	v, ok := f.entries.Get(key)
	if !ok {
		entry := &FlowTopEntry{agg: rec, updated: time.Now()}
		f.entries.Add(key, entry)
	} else {
		entry := v.(*FlowTopEntry)

		entry.updated = time.Now()
		entry.agg.EndedAt = rec.EndedAt
		entry.agg.NumMessages += rec.NumMessages
		entry.agg.NumBytes += rec.NumBytes
	}
}

func (f *FlowTop) Export() ([]*FlowTopEntry, error) {
	entries := make([]*FlowTopEntry, 0, f.entries.Len())

	keys := f.entries.Keys()

	for _, k := range keys {
		if v, ok := f.entries.Peek(k); ok {
			entries = append(entries, v.(*FlowTopEntry))
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		a := entries[i]
		b := entries[j]

		if a.agg.EndedAt == nil {
			if b.agg.EndedAt == nil {
				return a.updated.Before(b.updated)
			} else {
				return false
			}
		} else {
			if b.agg.EndedAt == nil {
				return true
			}

			return a.agg.EndedAt.Time().Before(b.agg.EndedAt.Time())
		}
	})

	return entries, nil
}

func (s *Server) checkOpsAllowed(ctx context.Context) bool {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}

	auth := md["authorization"]

	if len(auth) < 1 {
		return false
	}

	return auth[0] == s.opsToken
}

func (s *Server) CurrentFlowTop(ctx context.Context, req *pb.FlowTopRequest) (*pb.FlowTopSnapshot, error) {
	entries, err := s.flowTop.Export()
	if err != nil {
		return nil, err
	}

	if req.MaxRecords > 0 && len(entries) > int(req.MaxRecords) {
		entries = entries[:req.MaxRecords]
	}

	var snap pb.FlowTopSnapshot

	for _, e := range entries {
		snap.Records = append(snap.Records, e.agg)
	}

	return &snap, nil
}

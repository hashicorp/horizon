package hub

import (
	"context"
	"sync/atomic"

	"github.com/hashicorp/horizon/pkg/pb"
)

func (h *Hub) sendAgentInfoFlow(ctx context.Context, ai *agentConn) {
	var rec pb.FlowRecord

	rec.Agent = &pb.FlowRecord_AgentConnection{
		HubId:         h.id,
		AgentId:       ai.ID,
		Account:       ai.Account,
		StartedAt:     ai.Start,
		EndedAt:       ai.End,
		NumServices:   ai.Services,
		ActiveStreams: atomic.LoadInt64(ai.ActiveStreams),
	}

	h.cc.SendFlow(ctx, &rec)
}

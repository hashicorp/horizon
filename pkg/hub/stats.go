package hub

import (
	"sync/atomic"

	"github.com/hashicorp/horizon/pkg/pb"
)

func (h *Hub) sendAgentInfoFlow(ai *agentConn) {
	var rec pb.FlowRecord

	rec.Agent = &pb.FlowRecord_AgentConnection{
		HubId:         h.id,
		AgentId:       ai.ID,
		AccountId:     ai.AccountId,
		StartedAt:     ai.Start,
		NumServices:   ai.Services,
		TotalMessages: atomic.LoadInt64(ai.Messages),
		TotalBytes:    atomic.LoadInt64(ai.Bytes),
		TotalStreams:  atomic.LoadInt64(ai.Streams),
	}

	h.cc.SendFlow(&rec)
}

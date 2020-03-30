package hub

import (
	"github.com/hashicorp/horizon/pkg/edgeservices"
	"github.com/hashicorp/horizon/pkg/edgeservices/agents"
	"github.com/hashicorp/horizon/pkg/edgeservices/logs"
)

func (h *Hub) AddDefaultServices() {
	h.services.Register(edgeservices.Service{
		Host:    "agents.edge",
		Handler: agents.NewService(h.reg),
	})
}

func (h *Hub) AddLocalLogging(dir string) {
	local := &logs.LocalService{
		Dir: dir,
	}

	h.services.Register(edgeservices.Service{
		Host:    "logs.edge",
		Handler: local,
	})
}

package token

import "github.com/hashicorp/horizon/pkg/pb"

const (
	CapaConnect = "hzn:connect"
	CapaServe   = "hzn:serve"
	CapaAccess  = "hzn:access"
	CapaMgmt    = "hzn:mgmt"
	CapaConfig  = "hzn:config"
)

var CapabilityMapping = map[string]pb.Capability{
	CapaConnect: pb.CONNECT,
	CapaServe:   pb.SERVE,
	CapaAccess:  pb.ACCESS,
	CapaMgmt:    pb.MGMT,
	CapaConfig:  pb.CONFIG,
}

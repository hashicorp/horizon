package discovery

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/pb"
)

const HTTPPath = "/.well-known/horizon/hubs.json"

type GetNetlocs interface {
	GetAllNetworkLocations() ([]*pb.NetworkLocation, error)
}

type WellKnown struct {
	L          hclog.Logger
	GetNetlocs GetNetlocs
}

type DiscoveryData struct {
	ServerTime time.Time             `json:"server_time"`
	Hubs       []*pb.NetworkLocation `json:"hubs"`
}

func (wk *WellKnown) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	netlocs, err := wk.GetNetlocs.GetAllNetworkLocations()
	if err != nil {
		wk.L.Error("error getting network locations for well-known", "error", err)
		http.Error(w, "unable to find network locations", http.StatusInternalServerError)
		return
	}

	var dd DiscoveryData
	dd.ServerTime = time.Now()

	for _, loc := range netlocs {
		if loc.IsPublic() {
			dd.Hubs = append(dd.Hubs, loc)
		}
	}

	err = json.NewEncoder(w).Encode(&dd)
	if err != nil {
		wk.L.Error("error encoding discovery data for well-known", "error", err)
	}
}

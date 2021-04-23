package control

import (
	"encoding/json"
	fmt "fmt"
	"net"
	"net/http"
	"strings"

	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/discovery"
	"github.com/hashicorp/horizon/pkg/pb"
)

// GetAllNetworkLocations fetches all the hub records from the database and returns them.
// This is used by the discovery endpoint to give the agents the list of hubs to connect to.
// The list of available hubs is filtered through the hubs that are actually accessible via consul.
func (s *Server) GetAllNetworkLocations() ([]*pb.NetworkLocation, error) {
	var hubs []*Hub

	err := dbx.Check(s.db.Find(&hubs))
	if err != nil {
		return nil, err
	}

	var locs []*pb.NetworkLocation

	for _, h := range hubs {
		// The list of hubs in the database can be out of sync with the hubs that are actually
		// available currently. So we filter the list that comes out of the database through
		// the information we have about them from consul, excluding the ones that consul
		// says are not available.
		if s.hba != nil && !s.hba.HubAvailable(h.InstanceULID()) {
			continue
		}

		var hl []*pb.NetworkLocation

		err = json.Unmarshal(h.ConnectionInfo, &hl)
		if err != nil {
			return nil, err
		}

		for _, loc := range hl {
			loc.Name = h.StableIdULID().String() + "." + s.hubDomain
		}

		locs = append(locs, hl...)
	}

	return locs, nil
}

func (s *Server) setupRoutes() {
	s.mux.HandleFunc("/healthz", s.httpHealthz)
	s.mux.HandleFunc("/ip-info", s.httpIPInfo)
	s.mux.HandleFunc("/ulid", s.genUlid)

	// This discovery path is hit by the agents to find out the addresses of the hubs
	// themselves.
	var wk discovery.WellKnown
	wk.GetNetlocs = s

	s.mux.Handle(discovery.HTTPPath, &wk)
}

// ServeHTTP serves the HTTP handler that serves routes setup by setupRoutes
func (s *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	s.mux.ServeHTTP(w, req)
}

func (s *Server) httpHealthz(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(200)
}

// genUlid is a simple utility endpoint to generate a new ULID. It's just a nice
// to have when a new ULID is needed, one can be fetch from a control server.
func (s *Server) genUlid(w http.ResponseWriter, req *http.Request) {
	u := pb.NewULID()

	if req.Header.Get("Accept") == "application/json" {
		json.NewEncoder(w).Encode(map[string]string{
			"ulid": u.String(),
		})
	} else {
		fmt.Fprintln(w, u.String())
	}
}

func ipFromForwardedForHeader(v string) string {
	sep := strings.Index(v, ",")
	if sep == -1 {
		return v
	}
	return v[:sep]
}

var trustHeaders = []string{"X-Real-IP", "X-Forwarded-For"}

func ipFromRequest(r *http.Request) (net.IP, error) {
	remoteIP := ""
	for _, header := range trustHeaders {
		remoteIP = r.Header.Get(header)
		if http.CanonicalHeaderKey(header) == "X-Forwarded-For" {
			remoteIP = ipFromForwardedForHeader(remoteIP)
		}
		if remoteIP != "" {
			break
		}
	}

	if remoteIP == "" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return nil, err
		}
		remoteIP = host
	}

	ip := net.ParseIP(remoteIP)
	if ip == nil {
		return nil, fmt.Errorf("could not parse IP: %s", remoteIP)
	}
	return ip, nil
}

// Needs to mimic the ifconfig.co keys because that's the document schema
// that's expected.
type ipInfo struct {
	IP     string `json:"ip"`
	ASN    string `json:"asn,omitempty"`
	ASNOrg string `json:"asn_org,omitempty"`
}

// httpIPInfo extracts the client connection ip from the request and
// provides some information about that IP, such as ASN. This can be used
// by clients to optimize routing.
func (s *Server) httpIPInfo(w http.ResponseWriter, req *http.Request) {
	ip, err := ipFromRequest(req)
	if err != nil {
		w.WriteHeader(500)
		return
	}

	var info ipInfo
	info.IP = ip.String()

	if s.asnDB != nil {
		if asnInfo, err := s.asnDB.ASN(ip); err == nil {
			info.ASN = fmt.Sprintf("AS%d", asnInfo.AutonomousSystemNumber)
			info.ASNOrg = asnInfo.AutonomousSystemOrganization
		}
	}

	json.NewEncoder(w).Encode(&info)
}

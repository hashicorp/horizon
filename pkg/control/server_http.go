package control

import (
	"encoding/json"
	fmt "fmt"
	"net"
	"net/http"
	"strings"
)

func (s *Server) setupRoutes() {
	s.mux.HandleFunc("/healthz", s.httpHealthz)
	s.mux.HandleFunc("/ip-info", s.httpIPInfo)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	s.mux.ServeHTTP(w, req)
}

func (s *Server) httpHealthz(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(200)
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

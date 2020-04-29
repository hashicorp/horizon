package netloc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/hashicorp/go-cleanhttp"
	"github.com/hashicorp/horizon/pkg/pb"
)

func ec2MetaClient(endpoint string, timeout time.Duration) (*ec2metadata.EC2Metadata, error) {
	client := &http.Client{
		Timeout:   timeout,
		Transport: cleanhttp.DefaultTransport(),
	}

	c := aws.NewConfig().WithHTTPClient(client).WithMaxRetries(0)
	if endpoint != "" {
		c = c.WithEndpoint(endpoint)
	}

	session, err := session.NewSession(c)
	if err != nil {
		return nil, err
	}
	return ec2metadata.New(session, c), nil
}

var splitRe = regexp.MustCompile(`[\s,]+`)

func forgivingSplit(str string) []string {
	return splitRe.Split(str, -1)
}

var privateIPBlocks []*net.IPNet

func init() {
	for _, cidr := range []string{
		"127.0.0.0/8",    // IPv4 loopback
		"10.0.0.0/8",     // RFC1918
		"172.16.0.0/12",  // RFC1918
		"192.168.0.0/16", // RFC1918
		"169.254.0.0/16", // RFC3927 link-local
		"::1/128",        // IPv6 loopback
		"fe80::/10",      // IPv6 link-local
		"fc00::/7",       // IPv6 unique local addr
	} {
		_, block, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Errorf("parse error on %q: %v", cidr, err))
		}
		privateIPBlocks = append(privateIPBlocks, block)
	}
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}

	for _, block := range privateIPBlocks {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

func v4onlyDial() func(context.Context, string, string) (net.Conn, error) {
	return (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		DualStack: false,
		Control: func(network, address string, c syscall.RawConn) error {
			if network != "tcp4" {
				return io.EOF
			}
			return nil
		},
	}).DialContext
}

func Locate(defaultLabels *pb.LabelSet) ([]*pb.NetworkLocation, error) {
	mdc, err := ec2MetaClient("", 5*time.Second)
	if err != nil {
		return nil, err
	}

	var globalPub []*pb.Label

	labels := pb.ParseLabelSet("type=private")

	if defaultLabels != nil {
		labels.Labels = append(labels.Labels, defaultLabels.Labels...)
		globalPub = append([]*pb.Label{}, defaultLabels.Labels...)
	}

	privateAddrs := map[string]struct{}{}
	publicAddrs := map[string]struct{}{}
	publicAddrs6 := map[string]struct{}{}

	if id, err := mdc.GetMetadata("ami-id"); err == nil && id != "" {
		if ii, err := mdc.GetMetadata("instance-id"); err == nil && ii != "" {
			labels.Labels = append(labels.Labels, &pb.Label{
				Name:  "instance-id",
				Value: ii,
			})
		}

		if mac, err := mdc.GetMetadata("mac"); err == nil && mac != "" {
			if si, err := mdc.GetMetadata("network/interfaces/macs/" + mac + "/subnet-id"); err == nil && si != "" {
				labels.Labels = append(labels.Labels, &pb.Label{
					Name:  "subnet-id",
					Value: si,
				})
			}

			if vi, err := mdc.GetMetadata("network/interfaces/macs/" + mac + "/vpc-id"); err == nil && vi != "" {
				labels.Labels = append(labels.Labels, &pb.Label{
					Name:  "vpc-id",
					Value: vi,
				})

				globalPub = append(globalPub, &pb.Label{
					Name:  "vpc-id",
					Value: vi,
				})
			}

			ipKeys := []string{
				"network/interfaces/macs/" + mac + "/local-ipv4s",
			}

			for _, key := range ipKeys {
				if addrs, err := mdc.GetMetadata(key); err == nil && addrs != "" {
					for _, addr := range forgivingSplit(addrs) {
						if _, seen := privateAddrs[addr]; !seen {
							privateAddrs[addr] = struct{}{}
						}
					}
				}
			}

			ipKeys = []string{
				"network/interfaces/macs/" + mac + "/public-ipv4s",
			}

			for _, key := range ipKeys {
				if addrs, err := mdc.GetMetadata(key); err == nil && addrs != "" {
					for _, addr := range forgivingSplit(addrs) {
						if _, seen := privateAddrs[addr]; !seen {
							publicAddrs[addr] = struct{}{}
						}
					}
				}
			}

			ipKeys = []string{
				"network/interfaces/macs/" + mac + "/ipv6s",
			}

			for _, key := range ipKeys {
				if addrs, err := mdc.GetMetadata(key); err == nil && addrs != "" {
					for _, addr := range forgivingSplit(addrs) {
						if _, seen := publicAddrs6[addr]; !seen {
							publicAddrs6[addr] = struct{}{}
						}
					}
				}
			}

		}
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, err
		}

		for _, addr := range addrs {
			if ipaddr, ok := addr.(*net.IPNet); ok {
				ip := ipaddr.IP
				if _, seen := privateAddrs[ip.String()]; !seen {
					if ipv4 := ip.To4(); ipv4 != nil {
						if !ipv4.IsGlobalUnicast() {
							continue
						}

						if isPrivateIP(ipv4) {
							privateAddrs[ip.String()] = struct{}{}
							if _, seen := privateAddrs[ip.String()]; seen {
								continue
							}
						} else {
							publicAddrs[ip.String()] = struct{}{}
							if _, seen := publicAddrs[ip.String()]; seen {
								continue
							}
						}
					} else if ipv6 := ipaddr.IP.To16(); ipv6 != nil {
						if isPrivateIP(ipv6) {
							privateAddrs[ip.String()] = struct{}{}
							if _, seen := privateAddrs[ip.String()]; seen {
								continue
							}
						} else {
							publicAddrs6[ip.String()] = struct{}{}
							if _, seen := publicAddrs6[ip.String()]; seen {
								continue
							}
						}
					}
				}
			}
		}
	}

	pubLabels := pb.ParseLabelSet("type=public")
	pubLabels6 := pb.ParseLabelSet("type=public")

	pubLabels.Labels = append(pubLabels.Labels, globalPub...)
	pubLabels6.Labels = append(pubLabels6.Labels, globalPub...)

	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: cleanhttp.DefaultTransport(),
	}

	/*
			{
		  "ip": "2605:e000:151f:c3a6:815b:5ac8:7d4f:5797",
		  "ip_decimal": 50541168590408281122263404391741478807,
		  "country": "United States",
		  "country_eu": false,
		  "country_iso": "US",
		  "city": "Los Angeles",
		  "latitude": 34.0762,
		  "longitude": -118.3029,
		  "asn": "AS20001",
		  "asn_org": "TWC-20001-PACWEST",
		  "user_agent": {
		    "product": "Mozilla",
		    "version": "5.0",
		    "comment": "(Macintosh; Intel Mac OS X 10_15_4) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/81.0.4044.122 Safari/537.36",
		    "raw_value": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_4) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/81.0.4044.122 Safari/537.36"
		  }
		}
	*/

	var ifInfo struct {
		IP        string      `json:"ip"`
		ASN       string      `json:"asn"`
		Country   string      `json:"country_iso"`
		Latitude  json.Number `json:"latitude"`
		Longitude json.Number `json:"longitude"`
	}

	resp, err := client.Get("https://ifconfig.co/json")
	if err == nil {
		defer resp.Body.Close()

		err = json.NewDecoder(resp.Body).Decode(&ifInfo)
		if err == nil {
			ip := net.ParseIP(ifInfo.IP)
			if ip != nil {
				labels := []*pb.Label{
					{
						Name:  "asn",
						Value: ifInfo.ASN,
					},
					{
						Name:  "country",
						Value: ifInfo.Country,
					},
					{
						Name:  "location",
						Value: fmt.Sprintf("%s, %s", ifInfo.Latitude, ifInfo.Longitude),
					},
				}

				if ip.To16() != nil {
					if _, seen := publicAddrs6[ifInfo.IP]; !seen {
						publicAddrs6[ifInfo.IP] = struct{}{}
					}

					pubLabels6.Labels = append(pubLabels6.Labels, labels...)

					// Ok, do it again but v4 only

					tp := cleanhttp.DefaultTransport()
					tp.DialContext = v4onlyDial()
					client = &http.Client{
						Timeout:   5 * time.Second,
						Transport: tp,
					}

					resp, err := client.Get("https://ifconfig.co/json")
					if err == nil {
						defer resp.Body.Close()

						err = json.NewDecoder(resp.Body).Decode(&ifInfo)
						if err == nil {
							ip := net.ParseIP(ifInfo.IP)
							if ip != nil {
								if _, seen := publicAddrs[ifInfo.IP]; !seen {
									publicAddrs[ifInfo.IP] = struct{}{}
								}

								pubLabels.Labels = append(pubLabels.Labels, []*pb.Label{
									{
										Name:  "asn",
										Value: ifInfo.ASN,
									},
									{
										Name:  "country",
										Value: ifInfo.Country,
									},
									{
										Name:  "location",
										Value: fmt.Sprintf("%s, %s", ifInfo.Latitude, ifInfo.Longitude),
									},
								}...)
							}
						}
					}
				} else {
					if _, seen := publicAddrs[ifInfo.IP]; !seen {
						publicAddrs[ifInfo.IP] = struct{}{}
					}
					pubLabels.Labels = append(pubLabels.Labels, labels...)
				}
			}
		}
	}

	var netlocs []*pb.NetworkLocation

	labels.Finalize()
	pubLabels.Finalize()
	pubLabels6.Finalize()

	var addrs []string

	for addr := range privateAddrs {
		addrs = append(addrs, addr)
	}

	if len(addrs) != 0 {
		sort.Strings(addrs)

		netlocs = append(netlocs, &pb.NetworkLocation{
			Addresses: addrs,
			Labels:    labels,
		})
	}

	// Simplify the output if the v4 and v6 labels are the same (common case)
	if pubLabels.Equal(pubLabels6) {
		addrs = nil

		for addr := range publicAddrs {
			addrs = append(addrs, addr)
		}

		for addr := range publicAddrs6 {
			addrs = append(addrs, addr)
		}

		if len(addrs) != 0 {
			sort.Strings(addrs)

			netlocs = append(netlocs, &pb.NetworkLocation{
				Addresses: addrs,
				Labels:    pubLabels,
			})
		}
	} else {
		addrs = nil

		for addr := range publicAddrs {
			addrs = append(addrs, addr)
		}

		if len(addrs) != 0 {
			sort.Strings(addrs)

			netlocs = append(netlocs, &pb.NetworkLocation{
				Addresses: addrs,
				Labels:    pubLabels,
			})
		}

		addrs = nil

		for addr := range publicAddrs6 {
			addrs = append(addrs, addr)
		}

		if len(addrs) != 0 {
			sort.Strings(addrs)

			netlocs = append(netlocs, &pb.NetworkLocation{
				Addresses: addrs,
				Labels:    pubLabels6,
			})
		}
	}

	return netlocs, nil
}

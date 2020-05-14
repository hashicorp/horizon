package netloc

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"syscall"
	"time"

	"gortc.io/stun"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/hashicorp/go-cleanhttp"
	"github.com/hashicorp/horizon/pkg/pb"
)

// URL that will return a json document with ip, asn, asn_org, country_iso, latittude, longitude
// for the ip in question.
var LookupURL = "https://ifconfig.co/json"

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

var (
	privateIPBlocks []*net.IPNet
	ipv6Loopback    *net.IPNet
	ipv6LinkLocal   *net.IPNet
)

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

	_, ipv6Loopback, _ = net.ParseCIDR("::1/128")
	_, ipv6LinkLocal, _ = net.ParseCIDR("fe80::/10")
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

const (
	stunHost = "stun1.l.google.com"
	stunPort = "19302"
)

func gatherIPsViaStun(c chan []net.IP) {
	defer close(c)

	ips, err := net.LookupIP(stunHost)
	if err != nil {
		return
	}

	var mine []net.IP

	for _, ip := range ips {
		c, err := stun.Dial("udp", net.JoinHostPort(ip.String(), stunPort))
		if err != nil {
			continue
		}

		// Building binding request with random transaction id.
		message := stun.MustBuild(stun.TransactionID, stun.BindingRequest)

		var (
			inErr error
			ip    net.IP
		)

		// Sending request to STUN server, waiting for response message.
		err = c.Do(message, func(res stun.Event) {
			if res.Error != nil {
				inErr = res.Error
				return
			}

			// Decoding XOR-MAPPED-ADDRESS attribute from message.
			var xorAddr stun.XORMappedAddress
			if err := xorAddr.GetFrom(res.Message); err != nil {
				inErr = err
				return
			}

			ip = xorAddr.IP
		})

		if err != nil {
			continue
		}

		if inErr != nil {
			continue
		}

		mine = append(mine, ip)
	}

	c <- mine
}

type ec2Info struct {
	privateAddrs []string
	publicAddrs  []string
	publicAddrs6 []string

	privateLabels []*pb.Label
	publicLabels  []*pb.Label
}

func getEC2Labels(c chan *ec2Info) {
	defer close(c)

	mdc, err := ec2MetaClient("", 2*time.Second)
	if err != nil {
		return
	}

	info := &ec2Info{}

	if mdc.Available() {
		if ii, err := mdc.GetMetadata("instance-id"); err == nil && ii != "" {
			info.privateLabels = append(info.privateLabels, &pb.Label{
				Name:  "instance-id",
				Value: ii,
			})
		}

		if mac, err := mdc.GetMetadata("mac"); err == nil && mac != "" {
			if si, err := mdc.GetMetadata("network/interfaces/macs/" + mac + "/subnet-id"); err == nil && si != "" {
				info.privateLabels = append(info.privateLabels, &pb.Label{
					Name:  "subnet-id",
					Value: si,
				})
			}

			if vi, err := mdc.GetMetadata("network/interfaces/macs/" + mac + "/vpc-id"); err == nil && vi != "" {
				info.privateLabels = append(info.privateLabels, &pb.Label{
					Name:  "vpc-id",
					Value: vi,
				})

				info.publicLabels = append(info.publicLabels, &pb.Label{
					Name:  "vpc-id",
					Value: vi,
				})
			}

			ipKeys := []string{
				"network/interfaces/macs/" + mac + "/local-ipv4s",
			}

			for _, key := range ipKeys {
				if addrs, err := mdc.GetMetadata(key); err == nil && addrs != "" {
					info.privateAddrs = append(info.privateAddrs, forgivingSplit(addrs)...)
				}
			}

			ipKeys = []string{
				"network/interfaces/macs/" + mac + "/public-ipv4s",
			}

			for _, key := range ipKeys {
				if addrs, err := mdc.GetMetadata(key); err == nil && addrs != "" {
					info.publicAddrs = append(info.publicAddrs, forgivingSplit(addrs)...)
				}
			}

			ipKeys = []string{
				"network/interfaces/macs/" + mac + "/ipv6s",
			}

			for _, key := range ipKeys {
				if addrs, err := mdc.GetMetadata(key); err == nil && addrs != "" {
					info.publicAddrs6 = append(info.publicAddrs6, forgivingSplit(addrs)...)
				}
			}
		}
	}

	c <- info
}

// Sorts it's input
func uniq(input []string) []string {
	sort.Strings(input)

	var (
		out  []string
		last string
	)

	for _, s := range input {
		if s == last {
			continue
		}

		out = append(out, s)
		last = s
	}

	return out
}

func FastLocate(defaultLabels *pb.LabelSet) ([]*pb.NetworkLocation, error) {
	var privateAddrs, publicAddrs, publicAddrs6 []string

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
				if ipv4 := ip.To4(); ipv4 != nil {
					if !ipv4.IsGlobalUnicast() {
						continue
					}

					if isPrivateIP(ipv4) {
						privateAddrs = append(privateAddrs, ip.String())
					} else {
						publicAddrs = append(publicAddrs, ip.String())
					}
				} else if ipv6 := ipaddr.IP.To16(); ipv6 != nil {
					if ipv6Loopback.Contains(ipv6) || ipv6LinkLocal.Contains(ipv6) {
						continue
					}

					if isPrivateIP(ipv6) {
						privateAddrs = append(privateAddrs, ip.String())
					} else {
						publicAddrs6 = append(publicAddrs6, ip.String())
					}
				}
			}
		}
	}

	var (
		privateLabels = pb.ParseLabelSet("type=private")
		pubLabels     = pb.ParseLabelSet("type=public")
		pubLabels6    = pb.ParseLabelSet("type=public")
	)

	if defaultLabels != nil {
		privateLabels.Labels = append(privateLabels.Labels, defaultLabels.Labels...)
		pubLabels.Labels = append(pubLabels.Labels, defaultLabels.Labels...)
		pubLabels6.Labels = append(pubLabels6.Labels, defaultLabels.Labels...)
	}

	privateLabels.Finalize()
	pubLabels.Finalize()
	pubLabels6.Finalize()

	var netlocs []*pb.NetworkLocation

	privateAddrs = uniq(privateAddrs)
	publicAddrs = uniq(publicAddrs)
	publicAddrs6 = uniq(publicAddrs6)

	if len(privateAddrs) != 0 {
		netlocs = append(netlocs, &pb.NetworkLocation{
			Addresses: privateAddrs,
			Labels:    privateLabels,
		})
	}

	// Simplify the output if the v4 and v6 labels are the same (common case)
	if pubLabels.Equal(pubLabels6) {
		addrs := append(publicAddrs, publicAddrs6...)

		if len(addrs) != 0 {
			netlocs = append(netlocs, &pb.NetworkLocation{
				Addresses: addrs,
				Labels:    pubLabels,
			})
		}
	} else {
		if len(publicAddrs) != 0 {
			netlocs = append(netlocs, &pb.NetworkLocation{
				Addresses: publicAddrs,
				Labels:    pubLabels,
			})
		}

		if len(publicAddrs6) != 0 {
			netlocs = append(netlocs, &pb.NetworkLocation{
				Addresses: publicAddrs6,
				Labels:    pubLabels6,
			})
		}
	}

	return netlocs, nil
}

func Locate(defaultLabels *pb.LabelSet) ([]*pb.NetworkLocation, error) {
	ec2InfoC := make(chan *ec2Info, 1)

	go getEC2Labels(ec2InfoC)

	stunIPsC := make(chan []net.IP, 1)

	go gatherIPsViaStun(stunIPsC)

	var privateAddrs, publicAddrs, publicAddrs6 []string

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
				if ipv4 := ip.To4(); ipv4 != nil {
					if !ipv4.IsGlobalUnicast() {
						continue
					}

					if isPrivateIP(ipv4) {
						privateAddrs = append(privateAddrs, ip.String())
					} else {
						publicAddrs = append(publicAddrs, ip.String())
					}
				} else if ipv6 := ipaddr.IP.To16(); ipv6 != nil {
					if ipv6Loopback.Contains(ipv6) || ipv6LinkLocal.Contains(ipv6) {
						continue
					}

					if isPrivateIP(ipv6) {
						privateAddrs = append(privateAddrs, ip.String())
					} else {
						publicAddrs6 = append(publicAddrs6, ip.String())
					}
				}
			}
		}
	}

	var (
		privateLabels = pb.ParseLabelSet("type=private")
		pubLabels     = pb.ParseLabelSet("type=public")
		pubLabels6    = pb.ParseLabelSet("type=public")
	)

	if defaultLabels != nil {
		privateLabels.Labels = append(privateLabels.Labels, defaultLabels.Labels...)
		pubLabels.Labels = append(pubLabels.Labels, defaultLabels.Labels...)
		pubLabels6.Labels = append(pubLabels6.Labels, defaultLabels.Labels...)
	}

	if info, ok := <-ec2InfoC; ok {
		privateLabels.Labels = append(privateLabels.Labels, info.privateLabels...)
		pubLabels.Labels = append(pubLabels.Labels, info.publicLabels...)
		pubLabels6.Labels = append(pubLabels6.Labels, info.publicLabels...)

		privateAddrs = append(privateAddrs, info.privateAddrs...)
		publicAddrs = append(publicAddrs, info.publicAddrs...)
		publicAddrs6 = append(publicAddrs6, info.publicAddrs...)
	}

	if ips, ok := <-stunIPsC; ok {
		for _, ip := range ips {
			if ip.To4() != nil {
				publicAddrs = append(publicAddrs, ip.String())
			} else {
				publicAddrs6 = append(publicAddrs6, ip.String())
			}
		}
	}

	privateLabels.Finalize()
	pubLabels.Finalize()
	pubLabels6.Finalize()

	var netlocs []*pb.NetworkLocation

	privateAddrs = uniq(privateAddrs)
	publicAddrs = uniq(publicAddrs)
	publicAddrs6 = uniq(publicAddrs6)

	if len(privateAddrs) != 0 {
		netlocs = append(netlocs, &pb.NetworkLocation{
			Addresses: privateAddrs,
			Labels:    privateLabels,
		})
	}

	// Simplify the output if the v4 and v6 labels are the same (common case)
	if pubLabels.Equal(pubLabels6) {
		addrs := append(publicAddrs, publicAddrs6...)

		if len(addrs) != 0 {
			netlocs = append(netlocs, &pb.NetworkLocation{
				Addresses: addrs,
				Labels:    pubLabels,
			})
		}
	} else {
		if len(publicAddrs) != 0 {
			netlocs = append(netlocs, &pb.NetworkLocation{
				Addresses: publicAddrs,
				Labels:    pubLabels,
			})
		}

		if len(publicAddrs6) != 0 {
			netlocs = append(netlocs, &pb.NetworkLocation{
				Addresses: publicAddrs6,
				Labels:    pubLabels6,
			})
		}
	}

	return netlocs, nil
}

package hub

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/agent"
	"github.com/hashicorp/horizon/pkg/connect"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/discovery"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/testutils/central"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHub(t *testing.T) {
	t.Run("is registered in control server database", func(t *testing.T) {
		central.Dev(t, func(setup *central.DevSetup) {
			L := hclog.L()
			hub, err := NewHub(L, setup.ControlClient, setup.HubServToken)
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go hub.Run(ctx, setup.ClientListener)

			var netloc []*pb.NetworkLocation

			netloc = append(netloc, &pb.NetworkLocation{
				Addresses: []string{"127.0.0.1"},
				Labels:    pb.ParseLabelSet("dc=test"),
			})

			setup.ControlClient.SetLocations(netloc)

			ts := time.Now()
			setup.ControlClient.BootstrapConfig(ctx)

			time.Sleep(time.Second)

			var hr control.Hub

			err = dbx.Check(setup.DB.First(&hr))
			require.NoError(t, err)

			assert.Equal(t, setup.ControlClient.Id(), pb.ULIDFromBytes(hr.InstanceID))
			assert.True(t, hr.LastCheckin.After(ts))

			var outloc []*pb.NetworkLocation

			err = json.Unmarshal(hr.ConnectionInfo, &outloc)
			require.NoError(t, err)

			require.Equal(t, 1, len(outloc))

			assert.Equal(t, "127.0.0.1", outloc[0].Addresses[0])
		})
	})

	t.Run("can create and remove a service", func(t *testing.T) {
		central.Dev(t, func(setup *central.DevSetup) {
			L := hclog.L()
			hub, err := NewHub(L, setup.ControlClient, setup.HubServToken)
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go func() {
				err := hub.Run(ctx, setup.ClientListener)
				if err != nil {
					fmt.Printf("error in run: %s\n", err)
				}
			}()

			time.Sleep(time.Second)

			var clientTlsConfig tls.Config
			clientTlsConfig.InsecureSkipVerify = true
			clientTlsConfig.NextProtos = []string{"hzn"}

			cconn, err := tls.Dial("tcp", setup.HubAddr, &clientTlsConfig)
			require.NoError(t, err)

			serviceId := pb.NewULID()
			labels := pb.ParseLabelSet("service=www,env=prod")

			var preamble pb.Preamble
			preamble.Token = setup.AgentToken
			preamble.Services = []*pb.ServiceInfo{
				{
					ServiceId: serviceId,
					Type:      "test",
					Labels:    labels,
					Metadata: []*pb.KVPair{
						{
							Key:   "version",
							Value: "0.1x",
						},
					},
				},
			}

			fw, err := wire.NewFramingWriter(cconn)
			require.NoError(t, err)

			_, err = fw.WriteMarshal(1, &preamble)
			require.NoError(t, err)

			start := time.Now()

			fr, err := wire.NewFramingReader(cconn)
			require.NoError(t, err)

			var confirmation pb.Confirmation

			tag, _, err := fr.ReadMarshal(&confirmation)
			require.NoError(t, err)

			assert.Equal(t, uint8(1), tag)

			t.Logf("skew: %s", confirmation.Time.Time().Sub(start))

			assert.Equal(t, "connected", confirmation.Status)

			var so control.Service
			err = dbx.Check(setup.DB.First(&so))

			assert.NoError(t, err)

			assert.Equal(t, serviceId.Bytes(), so.ServiceId)
		})
	})

	t.Run("rejects hub tokens checks the serve capability", func(t *testing.T) {
		central.Dev(t, func(setup *central.DevSetup) {
			L := hclog.L()
			hub, err := NewHub(L, setup.ControlClient, setup.HubServToken)
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go hub.Run(ctx, setup.ClientListener)

			time.Sleep(time.Second)

			var clientTlsConfig tls.Config
			clientTlsConfig.InsecureSkipVerify = true
			clientTlsConfig.NextProtos = []string{"hzn"}

			cconn, err := tls.Dial("tcp", setup.HubAddr, &clientTlsConfig)
			require.NoError(t, err)

			serviceId := pb.NewULID()
			labels := pb.ParseLabelSet("service=www,env=prod")

			agentToken, err := setup.ControlServer.CreateToken(
				setup.MgmtCtx,
				&pb.CreateTokenRequest{
					Account: setup.Account,
				})
			require.NoError(t, err)

			var preamble pb.Preamble
			preamble.Token = agentToken.Token
			preamble.Services = []*pb.ServiceInfo{
				{
					ServiceId: serviceId,
					Type:      "test",
					Labels:    labels,
					Metadata: []*pb.KVPair{
						{
							Key:   "version",
							Value: "0.1x",
						},
					},
				},
			}

			fw, err := wire.NewFramingWriter(cconn)
			require.NoError(t, err)

			_, err = fw.WriteMarshal(1, &preamble)
			require.NoError(t, err)

			start := time.Now()

			fr, err := wire.NewFramingReader(cconn)
			require.NoError(t, err)

			var confirmation pb.Confirmation

			tag, _, err := fr.ReadMarshal(&confirmation)
			require.NoError(t, err)

			assert.Equal(t, uint8(1), tag)

			assert.Equal(t, "bad-token-capability", confirmation.Status)

			t.Logf("skew: %s", confirmation.Time.Time().Sub(start))

			var so control.Service
			err = dbx.Check(setup.DB.First(&so))

			assert.Error(t, err)
		})

	})

	t.Run("can send connections to other hubs", func(t *testing.T) {
		central.Dev(t, func(setup *central.DevSetup) {
			L := hclog.L()
			L.SetLevel(hclog.Trace)

			hub1, err := NewHub(L.Named("hub1"), setup.ControlClient, setup.HubServToken)
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go hub1.Run(ctx, setup.ClientListener)

			var netloc []*pb.NetworkLocation

			netloc = append(netloc, &pb.NetworkLocation{
				Addresses: []string{
					fmt.Sprintf("127.0.0.1:%d", setup.ClientListener.Addr().(*net.TCPAddr).Port),
				},
				Labels: pb.ParseLabelSet("dc=test"),
			})

			setup.ControlClient.SetLocations(netloc)

			setup.ControlClient.BootstrapConfig(ctx)

			go setup.ControlClient.Run(ctx)

			time.Sleep(time.Second)

			go hub1.Run(ctx, setup.ClientListener)

			time.Sleep(time.Second)

			g, err := agent.NewAgent(L.Named("agent"))
			require.NoError(t, err)

			g.Token = setup.AgentToken

			serviceId, err := g.AddService(&agent.Service{
				Type:    "test",
				Labels:  pb.ParseLabelSet("env=test,service=echo"),
				Handler: agent.EchoHandler(),
			})

			require.NoError(t, err)

			err = g.Start(ctx, discovery.HubConfigs(discovery.HubConfig{
				Addr:     setup.HubAddr,
				Insecure: true,
			}))
			require.NoError(t, err)

			go g.Wait(ctx)

			time.Sleep(time.Second)

			setup.NewControlClient(t, func(nc *control.Client, li net.Listener) {
				L = L.Named("testtest")

				L.Info("configuring second hub and agent")

				hub2, err := NewHub(L.Named("hub2"), nc, setup.HubServToken)
				require.NoError(t, err)

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				go hub2.Run(ctx, li)

				go nc.Run(ctx)

				time.Sleep(time.Second)

				sess, err := connect.Connect(L, li.Addr().String(), setup.AgentToken)
				require.NoError(t, err)

				L.Info("connecting to service")

				c, err := sess.ConnectToService(pb.ParseLabelSet("service=echo"))
				require.NoError(t, err)

				assert.Equal(t, serviceId, c.ServiceId())

				mb := wire.MarshalBytes("hello hzn from fed")

				err = c.WriteMarshal(30, &mb)
				require.NoError(t, err)

				var mb2 wire.MarshalBytes

				tag, err := c.ReadMarshal(&mb2)
				require.NoError(t, err)

				assert.Equal(t, byte(30), tag)
				assert.Equal(t, wire.MarshalBytes("hello hzn from fed"), mb2)
			})
		})
	})

	t.Run("can connect to services on other hubs", func(t *testing.T) {
		central.Dev(t, func(setup *central.DevSetup) {
			L := hclog.L()
			L.SetLevel(hclog.Trace)

			hub1, err := NewHub(L.Named("hub1"), setup.ControlClient, setup.HubServToken)
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go hub1.Run(ctx, setup.ClientListener)

			var netloc []*pb.NetworkLocation

			netloc = append(netloc, &pb.NetworkLocation{
				Addresses: []string{
					fmt.Sprintf("127.0.0.1:%d", setup.ClientListener.Addr().(*net.TCPAddr).Port),
				},
				Labels: pb.ParseLabelSet("dc=test"),
			})

			setup.ControlClient.SetLocations(netloc)

			setup.ControlClient.BootstrapConfig(ctx)

			go setup.ControlClient.Run(ctx)

			time.Sleep(time.Second)

			go hub1.Run(ctx, setup.ClientListener)

			time.Sleep(time.Second)

			g, err := agent.NewAgent(L.Named("agent"))
			require.NoError(t, err)

			g.Token = setup.AgentToken

			serviceId, err := g.AddService(&agent.Service{
				Type:    "test",
				Labels:  pb.ParseLabelSet("env=test,service=echo"),
				Handler: agent.EchoHandler(),
			})

			require.NoError(t, err)

			err = g.Start(ctx, discovery.HubConfigs(discovery.HubConfig{
				Addr:     setup.HubAddr,
				Insecure: true,
			}))
			require.NoError(t, err)

			go g.Wait(ctx)

			time.Sleep(time.Second)

			setup.NewControlClient(t, func(nc *control.Client, li net.Listener) {
				L = L.Named("testtest")

				L.Info("configuring second hub and agent")

				hub2, err := NewHub(L.Named("hub2"), nc, setup.HubServToken)
				require.NoError(t, err)

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				go nc.Run(ctx)

				time.Sleep(time.Second)

				go hub2.Run(ctx, li)

				sr := &pb.ServiceRoute{
					Hub:    hub1.id,
					Id:     serviceId,
					Type:   "test",
					Labels: pb.ParseLabelSet("service=echo"),
				}

				wctx, err := hub2.ConnectToService(ctx, sr, setup.Account, "echo", setup.HubServToken)
				require.NoError(t, err)

				mb := wire.MarshalBytes("hello hzn from fed")

				err = wctx.WriteMarshal(30, &mb)
				require.NoError(t, err)

				var mb2 wire.MarshalBytes

				tag, err := wctx.ReadMarshal(&mb2)
				require.NoError(t, err)

				assert.Equal(t, byte(30), tag)
				assert.Equal(t, wire.MarshalBytes("hello hzn from fed"), mb2)
			})
		})
	})
}

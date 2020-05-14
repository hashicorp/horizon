package agent

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/discovery"
	"github.com/hashicorp/horizon/pkg/hub"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/testutils/central"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/hashicorp/yamux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgent(t *testing.T) {
	t.Run("can connect and have a service connected to", func(t *testing.T) {
		central.Dev(t, func(setup *central.DevSetup) {

			L := hclog.New(&hclog.LoggerOptions{
				Name:  "dev",
				Level: hclog.Trace,
			})

			h, err := hub.NewHub(L.Named("hub"), setup.ControlClient, setup.HubServToken)
			require.NoError(t, err)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			go func() {
				err := h.Run(ctx, setup.ClientListener)
				require.NoError(t, err)
			}()

			time.Sleep(time.Second)

			agent, err := NewAgent(L.Named("agent"))
			require.NoError(t, err)

			agent.Token = setup.AgentToken

			serviceId, err := agent.AddService(&Service{
				Type:    "test",
				Labels:  pb.ParseLabelSet("env=test,service=echo"),
				Handler: EchoHandler(),
			})

			require.NoError(t, err)

			err = agent.Start(ctx, discovery.HubConfigs(discovery.HubConfig{
				Addr:     setup.HubAddr,
				Insecure: true,
			}))
			require.NoError(t, err)

			go agent.Wait(ctx)

			time.Sleep(time.Second)

			var so control.Service
			err = dbx.Check(setup.DB.First(&so))

			assert.NoError(t, err)

			assert.Equal(t, serviceId.Bytes(), so.ServiceId)

			// Ok, now try to connect to the service via the hub

			var clientTlsConfig tls.Config
			clientTlsConfig.InsecureSkipVerify = true
			clientTlsConfig.NextProtos = []string{"hzn"}

			cconn, err := tls.Dial("tcp", setup.HubAddr, &clientTlsConfig)
			require.NoError(t, err)

			var preamble pb.Preamble
			preamble.Token = setup.AgentToken

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

			bc := &wire.ComposedConn{
				Reader: fr.BufReader(),
				Writer: cconn,
				Closer: cconn,
			}

			cfg := yamux.DefaultConfig()
			cfg.EnableKeepAlive = true
			cfg.KeepAliveInterval = 30 * time.Second
			cfg.Logger = L.StandardLogger(&hclog.StandardLoggerOptions{
				InferLevels: true,
			})
			cfg.LogOutput = nil

			session, err := yamux.Client(bc, cfg)
			require.NoError(t, err)

			stream, err := session.OpenStream()
			require.NoError(t, err)

			fr2, err := wire.NewFramingReader(stream)
			require.NoError(t, err)

			fw2, err := wire.NewFramingWriter(stream)
			require.NoError(t, err)

			var conreq pb.ConnectRequest
			conreq.Target = pb.ParseLabelSet("service=echo")

			_, err = fw2.WriteMarshal(1, &conreq)
			require.NoError(t, err)

			var ack pb.ConnectAck

			tag, _, err = fr2.ReadMarshal(&ack)
			require.NoError(t, err)

			assert.Equal(t, uint8(1), tag)

			assert.Equal(t, serviceId, ack.ServiceId)

			mb := wire.MarshalBytes("hello hzn")

			_, err = fw2.WriteMarshal(30, &mb)
			require.NoError(t, err)

			var mb2 wire.MarshalBytes

			tag, _, err = fr2.ReadMarshal(&mb2)
			require.NoError(t, err)

			assert.Equal(t, uint8(30), tag)

			assert.Equal(t, []byte("hello hzn"), []byte(mb2))
		})
	})
}

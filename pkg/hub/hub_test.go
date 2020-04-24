package hub

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/testutils"
	"github.com/hashicorp/horizon/pkg/testutils/central"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHub(t *testing.T) {
	testutils.SetupDB()

	t.Run("can create and remove a service", func(t *testing.T) {
		central.Dev(t, func(setup *central.DevSetup) {
			L := hclog.L()
			hub, err := NewHub(L, setup.ControlClient)
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go hub.Run(ctx, setup.ClientListener)

			defer hub.WaitToDrain()

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
			hub, err := NewHub(L, setup.ControlClient)
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
					Account: &pb.Account{
						AccountId: setup.AccountId,
						Namespace: "/",
					},
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

}

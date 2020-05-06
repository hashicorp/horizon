package web_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/agent"
	"github.com/hashicorp/horizon/pkg/hub"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/hashicorp/horizon/pkg/testutils/central"
	"github.com/hashicorp/horizon/pkg/web"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeHTTPService struct {
	host string
}

func (f *fakeHTTPService) HandleRequest(ctx context.Context, L hclog.Logger, sctx agent.ServiceContext) error {
	var req pb.Request

	_, err := sctx.ReadMarshal(&req)
	if err != nil {
		return err
	}

	f.host = req.Host

	var resp pb.Response

	data, err := ioutil.ReadAll(sctx.Reader())
	if err != nil {
		return err
	}

	resp.Headers = []*pb.Header{
		{
			Name:  "X-Region",
			Value: []string{"test"},
		},
	}

	resp.Code = 247

	err = sctx.WriteMarshal(1, &resp)
	if err != nil {
		return err
	}

	w := sctx.Writer()
	fmt.Fprintf(w, "this is from the fake service: %s", string(data))
	return w.Close()
}

func TestWeb(t *testing.T) {
	central.Dev(t, func(setup *central.DevSetup) {
		L := hclog.L()
		hub, err := hub.NewHub(L, setup.ControlClient, setup.HubServToken)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go hub.Run(ctx, setup.ClientListener)

		time.Sleep(time.Second)

		a, err := agent.NewAgent(L)
		require.NoError(t, err)

		a.Token = setup.AgentToken

		var fe fakeHTTPService

		_, err = a.AddService(&agent.Service{
			Type:    "http",
			Labels:  pb.ParseLabelSet("env=test1,:deployment=aabbcc"),
			Handler: &fe,
		})
		require.NoError(t, err)

		err = a.Start(ctx, []agent.HubConfig{
			{
				Addr:     setup.HubAddr,
				Insecure: true,
			},
		})
		require.NoError(t, err)

		go a.Wait(ctx)

		time.Sleep(time.Second)

		t.Run("routes requests to the agent", func(t *testing.T) {
			name := "fuzz.localdomain"

			_, err := setup.ControlServer.AddLabelLink(setup.MgmtCtx,
				&pb.AddLabelLinkRequest{
					Labels:  pb.ParseLabelSet(":hostname=" + name),
					Account: setup.Account,
					Target:  pb.ParseLabelSet("env=test1"),
				})

			require.NoError(t, err)

			time.Sleep(time.Second)

			require.NoError(t, setup.ControlClient.ForceLabelLinkUpdate(ctx, L))

			f, err := web.NewFrontend(L, hub, setup.ControlClient, setup.HubServToken)
			require.NoError(t, err)

			req, err := http.NewRequest("GET", "http://"+name+"/", strings.NewReader("this is a request"))
			require.NoError(t, err)

			w := httptest.NewRecorder()

			f.ServeHTTP(w, req)

			assert.Equal(t, 247, w.Code)
			expected := "this is from the fake service: this is a request"
			assert.Equal(t, expected, w.Body.String())

			assert.Equal(t, "fuzz.localdomain", fe.host)
		})

		t.Run("supports deployment routes", func(t *testing.T) {
			prefixHost := "fuzz-.localdomain"

			_, err = setup.ControlServer.AddLabelLink(setup.MgmtCtx,
				&pb.AddLabelLinkRequest{
					Labels:  pb.ParseLabelSet(":hostname=" + prefixHost),
					Account: setup.Account,
					Target:  pb.ParseLabelSet("env=test1"),
				})

			require.NoError(t, err)

			time.Sleep(time.Second)

			require.NoError(t, setup.ControlClient.ForceLabelLinkUpdate(ctx, L))

			target := "fuzz-aabbcc.localdomain"

			f, err := web.NewFrontend(L, hub, setup.ControlClient, setup.HubServToken)
			require.NoError(t, err)

			req, err := http.NewRequest("GET", "http://"+target+"/", strings.NewReader("this is a request"))
			require.NoError(t, err)

			w := httptest.NewRecorder()

			f.ServeHTTP(w, req)

			assert.Equal(t, 247, w.Code)
			expected := "this is from the fake service: this is a request"
			assert.Equal(t, expected, w.Body.String())
		})
	})
}

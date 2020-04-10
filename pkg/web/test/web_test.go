package web_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/agent"
	"github.com/hashicorp/horizon/pkg/data"
	"github.com/hashicorp/horizon/pkg/hub"
	"github.com/hashicorp/horizon/pkg/noiseconn"
	"github.com/hashicorp/horizon/pkg/registry"
	"github.com/hashicorp/horizon/pkg/web"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeHTTPService struct {
}

func (f *fakeHTTPService) HandleRequest(ctx context.Context, L hclog.Logger, sctx agent.ServiceContext) error {
	var resp wire.Response

	data, err := ioutil.ReadAll(sctx.Reader())
	if err != nil {
		return err
	}

	resp.Headers = []*wire.Header{
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
	dir, err := ioutil.TempDir("", "agent")
	require.NoError(t, err)

	defer os.RemoveAll(dir)

	l, err := net.Listen("tcp", ":0")
	require.NoError(t, err)

	defer l.Close()

	db, err := data.NewBolt(filepath.Join(dir, "db.db"))
	require.NoError(t, err)

	L := hclog.L()
	reg, err := registry.NewRegistry(registry.RandomKey(), ".localhost", db)

	acc, name, err := reg.AddAccount(L)
	require.NoError(t, err)

	token, err := reg.Token(L, acc)
	require.NoError(t, err)

	dkey, err := noiseconn.GenerateKey()
	require.NoError(t, err)

	h, err := hub.NewHub(L.Named("hub"), reg, dkey)
	require.NoError(t, err)

	h.AddDefaultServices()

	addr := l.Addr().(*net.TCPAddr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go h.Serve(ctx, l)

	gkey1, err := noiseconn.GenerateKey()
	require.NoError(t, err)

	g1, err := agent.NewAgent(L.Named("g1"), gkey1)
	require.NoError(t, err)

	g1.Token = token

	_, err = g1.AddService(&agent.Service{
		Type: "http",
		Labels: [][]agent.Label{
			{
				{Name: "env", Value: "test1"},
			},
			{
				{Name: "env", Value: "test1"},
				{Name: ":deployment", Value: "aabbcc"},
			},
		},
		Description: "a fake http service",
		Handler:     &fakeHTTPService{},
	})

	require.NoError(t, err)

	g1.Start(ctx, []agent.HubConfig{
		{
			Addr:      addr.String(),
			PublicKey: noiseconn.PublicKey(dkey),
		},
	})

	t.Run("routes requests to the agent", func(t *testing.T) {
		reg.AddLabelLink(acc.String(), []string{":hostname=" + name}, []string{"env=test1"})

		var f web.Frontend
		f.L = L
		f.Connector = h
		f.LabelResolver = reg
		f.Checker = reg

		req, err := http.NewRequest("GET", "http://"+name+"/", strings.NewReader("this is a request"))
		require.NoError(t, err)

		w := httptest.NewRecorder()

		f.ServeHTTP(w, req)

		assert.Equal(t, 247, w.Code)
		expected := "this is from the fake service: this is a request"
		assert.Equal(t, expected, w.Body.String())
	})

	t.Run("supports deployment routes", func(t *testing.T) {
		dot := strings.IndexByte(name, '.')

		prefixHost := name[:dot] + "-" + name[dot:]
		reg.AddLabelLink(acc.String(), []string{":hostname=" + prefixHost, ":deployment=true"}, []string{"env=test1"})

		target := name[:dot] + "-aabbcc" + name[dot:]

		var f web.Frontend
		f.L = L
		f.Connector = h
		f.LabelResolver = reg
		f.Checker = reg

		req, err := http.NewRequest("GET", "http://"+target+"/", strings.NewReader("this is a request"))
		require.NoError(t, err)

		w := httptest.NewRecorder()

		f.ServeHTTP(w, req)

		assert.Equal(t, 247, w.Code)
		expected := "this is from the fake service: this is a request"
		assert.Equal(t, expected, w.Body.String())
	})

}

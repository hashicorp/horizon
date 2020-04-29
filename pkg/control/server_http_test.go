package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/oschwald/geoip2-golang"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerHTTP(t *testing.T) {
	t.Run("can perform asn lookups on the ip", func(t *testing.T) {
		path := filepath.Join("..", "..", "tmp", "GeoLite2-ASN.mmdb")

		_, err := os.Stat(path)
		if err != nil {
			t.Skip("missing geolite database")
		}

		var s Server

		db, err := geoip2.Open(path)
		require.NoError(t, err)

		s.asnDB = db

		req, err := http.NewRequest("GET", "/ip-info", nil)
		require.NoError(t, err)

		req.Header.Add("X-Real-IP", "1.1.1.1")

		w := httptest.NewRecorder()
		s.httpIPInfo(w, req)

		require.Equal(t, 200, w.Code)

		var info ipInfo

		err = json.Unmarshal(w.Body.Bytes(), &info)
		require.NoError(t, err)

		assert.Equal(t, "AS13335", info.ASN)
		assert.Equal(t, "CLOUDFLARENET", info.ASNOrg)
	})
}

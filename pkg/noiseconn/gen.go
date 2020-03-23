package noiseconn

import (
	"encoding/json"
	fmt "fmt"
	"net/http"
	strings "strings"
)

type TunnelOptions struct {
	Host        string
	Tags        map[string]string
	Description string
}

type TunnelParams struct {
	TunnelID         string
	TunnelARN        string
	SourceToken      string
	DestinationToken string
}

func CreateTunnel(opts TunnelOptions) (*TunnelParams, error) {
	host := opts.Host

	scheme := "https"

	if host == "localhost" || strings.HasPrefix(host, "localhost:") {
		scheme = "http"
	}

	resp, err := http.Get(fmt.Sprintf("%s://%s/create-tunnel", scheme, host))
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	var response struct {
		TunnelID    string `json:"tunnel-id"`
		SourceToken string `json:"source-token"`
		DestToken   string `json:"destination-token"`
	}

	json.NewDecoder(resp.Body).Decode(&response)

	var params TunnelParams

	params.TunnelID = response.TunnelID
	params.TunnelARN = response.TunnelID
	params.SourceToken = response.SourceToken
	params.DestinationToken = response.DestToken

	return &params, nil
}

func DeleteTunnel(str string) error {
	token, _, _, err := DecodeToken(str)
	if err != nil {
		return err
	}

	host := token.Host

	scheme := "https"

	if host == "localhost" || strings.HasPrefix(host, "localhost:") {
		scheme = "http"
	}

	url := fmt.Sprintf("%s://%s/tunnel", scheme, host)

	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return err
	}

	req.Header.Add("access-token", str)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}

	resp.Body.Close()

	if resp.StatusCode != 204 {
		return fmt.Errorf("error deleting tunnel: %d", resp.StatusCode)
	}

	return nil
}

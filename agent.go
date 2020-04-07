package horizon

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/flynn/noise"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/agent"
	"github.com/hashicorp/horizon/pkg/edgeservices/logs"
	"github.com/hashicorp/horizon/pkg/noiseconn"
	"github.com/hashicorp/horizon/pkg/token"
	"github.com/pkg/errors"
)

// A horizon agent designed to be embedded within another application
type Agent struct {
	cfg   AgentConfig
	agent *agent.Agent
}

// Configuration required to create an agent
type AgentConfig struct {
	// Previously created key to use for the agent. If blank, a new key will be created.
	Key string

	// Token to access the horizon hub node or cluster
	Token string

	// Address of the hub to connect to. If blank, attempt to read an address encoded in the token
	HubAddress string

	// Public key of the hub that will be connected to. If blank, attemp to read the public key encoded in the token
	HubPublicKey string

	// Logger to use for subcomponents
	Logger hclog.Logger

	// Labels to add to all registered services
	DefaultLabels []agent.Label

	dhKey noise.DHKey
}

// Validate the configuration
func (cfg AgentConfig) Validate() error {
	if cfg.Key != "" {
		key, err := noiseconn.ParsePrivateKey(cfg.Key)
		if err != nil {
			return err
		}

		cfg.dhKey = key
	} else {
		key, err := noiseconn.GenerateKey()
		if err != nil {
			return err
		}

		cfg.dhKey = key
	}

	if cfg.Logger == nil {
		cfg.Logger = hclog.L()
	}

	if cfg.Token == "" {
		return fmt.Errorf("no token provided")
	}

	md, err := token.Metadata(cfg.Token)
	if err != nil {
		return errors.Wrapf(err, "error decoding token")
	}

	if cfg.HubAddress == "" {
		cfg.HubAddress = md["hub-addr"]

		if cfg.HubAddress == "" {
			return fmt.Errorf("no hub address found in config or token")
		}
	}

	if cfg.HubPublicKey == "" {
		cfg.HubPublicKey = md["emhub-key"]

		if cfg.HubPublicKey == "" {
			return fmt.Errorf("no hub public key found in config or token")
		}
	}

	return nil
}

// Generate a new, random key used to encrypt traffic to the hub
func GenerateKey() (string, error) {
	key, err := noiseconn.GenerateKey()
	if err != nil {
		return "", err
	}

	return noiseconn.PrivateKey(key), nil
}

// Create a new agent using the given configuration
func NewEmbeddedAgent(cfg AgentConfig) (*Agent, error) {
	err := cfg.Validate()
	if err != nil {
		return nil, err
	}

	agent, err := agent.NewAgent(cfg.Logger.Named("agent"), cfg.dhKey)
	if err != nil {
		return nil, err
	}

	agent.Token = cfg.Token

	return &Agent{
		cfg:   cfg,
		agent: agent,
	}, nil

}

// Add a service that will be advertised by this agent
func (em *Agent) AddService(serv *agent.Service) error {
	if len(em.cfg.DefaultLabels) > 0 {
		serv.Labels = CombineLabels(serv.Labels, em.cfg.DefaultLabels)
	}

	_, err := em.agent.AddService(serv)
	return err
}

// Add an HTTP service to be advertised by this agent. HTTP requests are sent to the
// HTTP server located at +url+.
func (em *Agent) AddHTTPService(labels []agent.Label, url, description string) error {
	var serv agent.Service

	serv.Type = "http"
	serv.Labels = labels
	serv.Description = description
	serv.Handler = agent.HTTPHandler(url)

	if len(em.cfg.DefaultLabels) > 0 {
		serv.Labels = CombineLabels(serv.Labels, em.cfg.DefaultLabels)
	}

	_, err := em.agent.AddService(&serv)
	return err
}

// Create a new log writer that will transmit each individual line as a log message. The
// +key+ argument should identify the logs being emitted, be them per agent, per service,
// or otherwise.
func (em *Agent) LogWriter(key string) (io.Writer, error) {
	lt, err := em.agent.OpenLogTransmitter(key)
	if err != nil {
		return nil, err
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	go func() {
		defer pr.Close()

		br := bufio.NewReader(pr)

		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}

			lt.Transmit(&logs.Message{
				Timestamp: logs.Now(),
				Mesg:      strings.TrimRight(line, " \t\n"),
			})
		}
	}()

	return pw, nil

}

// Start the agent by connecting to the hub and processing requests
func (em *Agent) Start(ctx context.Context) error {
	return em.agent.Start(ctx, []agent.HubConfig{
		{
			Addr:      em.cfg.HubAddress,
			PublicKey: em.cfg.HubPublicKey,
		},
	})
}

// Wait for the agent to finish all work
func (em *Agent) Wait(ctx context.Context) error {
	return em.agent.Wait(ctx)
}

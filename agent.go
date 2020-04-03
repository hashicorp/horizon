package horizon

import (
	"context"
	"fmt"

	"github.com/flynn/noise"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/agent"
	"github.com/hashicorp/horizon/pkg/noiseconn"
	"github.com/hashicorp/horizon/pkg/token"
	"github.com/pkg/errors"
)

// A horizon agent designed to be embedded within another application
type EmbeddedAgent struct {
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
	DefaultLabels []string

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
func NewEmbeddedAgent(cfg AgentConfig) (*EmbeddedAgent, error) {
	err := cfg.Validate()
	if err != nil {
		return nil, err
	}

	agent, err := agent.NewAgent(cfg.Logger.Named("agent"), cfg.dhKey)
	if err != nil {
		return nil, err
	}

	agent.Token = cfg.Token

	return &EmbeddedAgent{
		cfg:   cfg,
		agent: agent,
	}, nil

}

type (
	Service        = agent.Service
	ServiceHandler = agent.ServiceHandler
)

// Add a service that will be advertised by this agent
func (em *EmbeddedAgent) AddService(serv *Service) error {
	if len(em.cfg.DefaultLabels) > 0 {
		serv.Labels = CombineLabels(serv.Labels, em.cfg.DefaultLabels)
	}

	_, err := em.agent.AddService(serv)
	return err
}

// Add an HTTP service to be advertised by this agent. HTTP requests are sent to the
// HTTP server located at +url+.
func (em *EmbeddedAgent) AddHTTPService(labels []string, url, description string) error {
	var serv Service

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

// Run the agent by connecting to the hub and processing requests
func (em *EmbeddedAgent) Run(ctx context.Context) error {
	return em.agent.Start(ctx, []agent.HubConfig{
		{
			Addr:      em.cfg.HubAddress,
			PublicKey: em.cfg.HubPublicKey,
		},
	})
}

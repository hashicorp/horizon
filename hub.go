package horizon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/flynn/noise"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/control"
	"github.com/hashicorp/horizon/pkg/hub"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/oklog/ulid"
	"github.com/pkg/errors"
)

// Type which contains a horizon hub instance configured to be embedded within
// another application as a simple, single server configuration.
type Hub struct {
	cfg HubConfig

	h *hub.Hub

	l     net.Listener
	acc   ulid.ULID
	dhkey noise.DHKey
}

// Configuration provided to create a new EmbeddedHub
type HubConfig struct {
	// An address to listen on. For instance ":23442"
	Address string

	// Token to connect to central tier cluster
	Token string

	// The path on disk to store the hub's configuration
	Path string

	// The path to store logs under. Defaults to underneith Path if not set
	LogPath string

	// Domain name under which agents will be given unique hostnames
	Domain string

	// Logger to use for subcomponents
	Logger hclog.Logger

	// S3 bucket that will be used for routing updates
	S3Bucket string
}

// Check the configuration to make sure it's valid
func (c HubConfig) Validate() error {
	fi, err := os.Stat(c.Path)
	if err != nil {
		return errors.Wrapf(err, "error with Path: %s", c.Path)
	}

	if !fi.IsDir() {
		return fmt.Errorf("path was not directory: %s", c.Path)
	}

	_, _, err = net.SplitHostPort(c.Address)
	if err != nil {
		return errors.Wrapf(err, "error with Address: %s", c.Address)
	}

	if c.Logger == nil {
		c.Logger = hclog.L()
	}

	if c.LogPath == "" {
		c.LogPath = filepath.Join(c.Path, "logs")
		err = os.Mkdir(c.LogPath, 0755)
		if err != nil {
			return err
		}
	} else {
		fi, err := os.Stat(c.Path)
		if err != nil {
			return errors.Wrapf(err, "error with Path: %s", c.Path)
		}

		if !fi.IsDir() {
			return fmt.Errorf("path was not directory: %s", c.Path)
		}
	}

	return nil
}

// Create a new Hub instance for use embedded in another program.
func NewHub(ctx context.Context, cfg HubConfig) (*Hub, error) {
	err := cfg.Validate()
	if err != nil {
		return nil, err
	}

	sess := session.New()

	id := pb.NewULID()

	client, err := control.NewClient(ctx, control.ClientConfig{
		Id:       id,
		Token:    cfg.Token,
		Version:  "test",
		Session:  sess,
		S3Bucket: cfg.S3Bucket,
		WorkDir:  cfg.Path,
	})

	h, err := hub.NewHub(cfg.Logger.Named("hub"), client)
	if err != nil {
		return nil, err
	}

	em := &Hub{
		cfg: cfg,

		h: h,
	}

	return em, nil
}

/*
// Generate a new token to access this hub
func (em *Hub) GenerateToken() (string, error) {
	metadata := map[string]string{
		"emhub-key": noiseconn.PublicKey(em.dhkey),
	}

	return em.reg.TokenWithMetadata(em.cfg.Logger, em.acc, metadata)
}

// Generate a new token to access this hub with the address of the hub embedded
// in the token. This allows just the token to be given to the agents to make a connection.
func (em *Hub) GenerateTokenWithAddress(addr string) (string, error) {
	metadata := map[string]string{
		"emhub-key":   noiseconn.PublicKey(em.dhkey),
		"hub-address": addr,
	}

	return em.reg.TokenWithMetadata(em.cfg.Logger, em.acc, metadata)
}
*/

// Begin listened for connections
func (em *Hub) Serve(ctx context.Context) error {
	return em.h.Serve(ctx, em.l)
}

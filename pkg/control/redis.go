package control

import (
	context "context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-redis/redis/v8"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/mr-tron/base58"
	"golang.org/x/crypto/blake2b"
)

type RedisServer struct {
	rc *redis.Client
}

func NewRedisServer(ctx context.Context, opts *redis.Options) (*RedisServer, error) {
	rc := redis.NewClient(opts)

	err := rc.Ping(ctx).Err()
	if err != nil {
		return nil, err
	}

	rs := &RedisServer{rc: rc}

	return rs, nil
}

func (r *RedisServer) AddService(ctx context.Context, service *pb.ServiceRequest) error {
	acc := service.Account.SpecString()

	h, _ := blake2b.New256(nil)

	data, err := json.Marshal(service)
	if err != nil {
		return err
	}

	h.Write(data)

	serviceKey := "service:" + base58.Encode(data)

	err = r.rc.Set(ctx, serviceKey, string(data), 0).Err()
	if err != nil {
		return err
	}

	// Store the label hash => service key in a hashi
	// On looup, for every label pair in the target label, make requests to the hash.
	//

	for _, lbl := range service.Labels.Labels {
		h.Reset()
		fmt.Fprintf(h, "%s:%s=%s", acc, strings.ToLower(lbl.Name), strings.ToLower(lbl.Value))

		key := "route:" + base58.Encode(h.Sum(nil))

		err = r.rc.Set(ctx, key, serviceKey, 0).Err()
		if err != nil {
			return err
		}
	}

	return nil
}

module github.com/hashicorp/horizon

go 1.13

require (
	github.com/armon/go-metrics v0.3.3
	github.com/aws/aws-sdk-go v1.25.41
	github.com/caddyserver/certmagic v0.10.3
	github.com/davecgh/go-spew v1.1.1
	github.com/frankban/quicktest v1.4.1 // indirect
	github.com/go-acme/lego/v3 v3.5.0
	github.com/go-redis/redis/v8 v8.11.3
	github.com/gogo/protobuf v1.3.1
	github.com/golang-migrate/migrate/v4 v4.10.0
	github.com/golang/protobuf v1.5.2
	github.com/hashicorp/consul/api v1.7.0
	github.com/hashicorp/go-cleanhttp v0.5.1
	github.com/hashicorp/go-hclog v0.13.0
	github.com/hashicorp/go-immutable-radix v1.2.0 // indirect
	github.com/hashicorp/go-multierror v1.1.0
	github.com/hashicorp/go-uuid v1.0.2 // indirect
	github.com/hashicorp/golang-lru v0.5.3
	github.com/hashicorp/vault/api v1.0.5-0.20190909201928-35325e2c3262
	github.com/hashicorp/yamux v0.0.0-20210316155119-a95892c5f864
	github.com/imdario/mergo v0.3.8 // indirect
	github.com/jinzhu/gorm v1.9.12
	github.com/klauspost/compress v1.10.10
	github.com/lib/pq v1.3.0
	github.com/mitchellh/cli v1.1.0
	github.com/mitchellh/go-homedir v1.1.0
	github.com/mitchellh/go-server-timing v1.0.0
	github.com/mitchellh/go-testing-interface v1.0.0
	github.com/mitchellh/mapstructure v1.1.2
	github.com/mr-tron/base58 v1.1.3
	github.com/oklog/ulid v1.3.1
	github.com/oschwald/geoip2-golang v1.4.0
	github.com/pierrec/lz4 v2.2.6+incompatible
	github.com/pierrec/lz4/v3 v3.3.2
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.4.0
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.5.1
	go.etcd.io/bbolt v1.3.3
	golang.org/x/crypto v0.0.0-20200622213623-75b288015ac9
	golang.org/x/net v0.0.0-20210825183410-e898025ed96a
	golang.org/x/time v0.0.0-20200630173020-3af7569d3a1e
	google.golang.org/genproto v0.0.0-20200416231807-8751e049a2a0 // indirect
	google.golang.org/grpc v1.28.1
	gopkg.in/square/go-jose.v2 v2.4.1 // indirect
	gortc.io/stun v1.22.2
	k8s.io/apimachinery v0.18.0
	k8s.io/client-go v0.18.0
)

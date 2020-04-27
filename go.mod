module github.com/hashicorp/horizon

go 1.13

require (
	cirello.io/dynamolock v1.3.3
	github.com/DATA-DOG/go-txdb v0.1.3
	github.com/DataDog/zstd v1.4.4
	github.com/armon/go-metrics v0.3.3
	github.com/aws/aws-sdk-go v1.25.41
	github.com/caddyserver/certmagic v0.10.3
	github.com/davecgh/go-spew v1.1.1
	github.com/fatih/color v1.9.0 // indirect
	github.com/flynn/noise v0.0.0-20180327030543-2492fe189ae6
	github.com/frankban/quicktest v1.4.1 // indirect
	github.com/go-acme/lego/v3 v3.5.0
	github.com/go-sql-driver/mysql v1.5.0 // indirect
	github.com/gogo/protobuf v1.3.1
	github.com/golang/protobuf v1.4.0 // indirect
	github.com/hashicorp/go-hclog v0.12.1
	github.com/hashicorp/go-immutable-radix v1.2.0 // indirect
	github.com/hashicorp/go-multierror v1.1.0
	github.com/hashicorp/go-rootcerts v1.0.2 // indirect
	github.com/hashicorp/go-uuid v1.0.2 // indirect
	github.com/hashicorp/golang-lru v0.5.3
	github.com/hashicorp/vault/api v1.0.4
	github.com/hashicorp/yamux v0.0.0-20190923154419-df201c70410d
	github.com/jinzhu/gorm v1.9.12
	github.com/lib/pq v1.3.0
	github.com/mattn/go-colorable v0.1.6 // indirect
	github.com/mitchellh/mapstructure v1.1.2
	github.com/mr-tron/base58 v1.1.3
	github.com/oklog/ulid v1.3.1
	github.com/pierrec/lz4 v2.2.6+incompatible
	github.com/pierrec/lz4/v3 v3.3.2
	github.com/pkg/errors v0.9.1
	github.com/stretchr/testify v1.5.1
	github.com/y0ssar1an/q v1.0.10
	go.etcd.io/bbolt v1.3.3
	golang.org/x/crypto v0.0.0-20200317142112-1b76d66859c6
	golang.org/x/net v0.0.0-20200324143707-d3edc9973b7e
	golang.org/x/sync v0.0.0-20200317015054-43a5402ce75a // indirect
	golang.org/x/sys v0.0.0-20200413165638-669c56c373c4 // indirect
	google.golang.org/genproto v0.0.0-20200416231807-8751e049a2a0 // indirect
	google.golang.org/grpc v1.28.1
	gopkg.in/square/go-jose.v2 v2.4.1 // indirect
	gopkg.in/yaml.v2 v2.2.8 // indirect
)

replace (
	github.com/hashicorp/memberlist => ../memberlist
	github.com/hashicorp/serf => ../serf
	github.com/hashicorp/yamux => ../yamux
)

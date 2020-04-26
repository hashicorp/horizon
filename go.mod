module github.com/hashicorp/horizon

go 1.13

require (
	cirello.io/dynamolock v1.3.3
	github.com/DATA-DOG/go-txdb v0.1.3
	github.com/DataDog/zstd v1.4.1
	github.com/NYTimes/gziphandler v1.1.1 // indirect
	github.com/Nvveen/Gotty v0.0.0-20120604004816-cd527374f1e5 // indirect
	github.com/SAP/go-hdb v0.14.4 // indirect
	github.com/SermoDigital/jose v0.9.1 // indirect
	github.com/armon/go-metrics v0.3.3
	github.com/asaskevich/govalidator v0.0.0-20200108200545-475eaeb16496 // indirect
	github.com/aws/aws-sdk-go v1.25.12
	github.com/aws/aws-sdk-go-v2 v0.20.0
	github.com/caddyserver/certmagic v0.10.3
	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
	github.com/containerd/continuity v0.0.0-20200413184840-d3ef23f19fbb // indirect
	github.com/davecgh/go-spew v1.1.1
	github.com/dghubble/trie v0.0.0-20200219060618-c42a287caf69
	github.com/dgraph-io/badger v1.6.1 // indirect
	github.com/dgraph-io/badger/v2 v2.0.3
	github.com/dgryski/go-metro v0.0.0-20180109044635-280f6062b5bc // indirect
	github.com/docker/go-connections v0.4.0 // indirect
	github.com/docker/go-units v0.4.0 // indirect
	github.com/duosecurity/duo_api_golang v0.0.0-20200413152554-59113bce2c7a // indirect
	github.com/dustinkirkland/golang-petname v0.0.0-20191129215211-8e5a1ed0cff0
	github.com/elazarl/go-bindata-assetfs v1.0.0 // indirect
	github.com/flynn/noise v0.0.0-20180327030543-2492fe189ae6
	github.com/go-acme/lego v2.7.2+incompatible
	github.com/go-acme/lego/v3 v3.5.0
	github.com/gocql/gocql v0.0.0-20200410100145-b454769479c6 // indirect
	github.com/gogo/protobuf v1.3.1
	github.com/golang/protobuf v1.3.4
	github.com/hashicorp/go-hclog v0.12.1
	github.com/hashicorp/go-memdb v1.2.0 // indirect
	github.com/hashicorp/go-multierror v1.1.0
	github.com/hashicorp/memberlist v0.2.0
	github.com/hashicorp/nomad/api v0.0.0-20200414183643-417f50f925b6
	github.com/hashicorp/serf v0.9.0
	github.com/hashicorp/terraform v0.12.24
	github.com/hashicorp/uuid v0.0.0-20160311170451-ebb0a03e909c
	github.com/hashicorp/vault v0.10.4
	github.com/hashicorp/yamux v0.0.0-20190923154419-df201c70410d
	github.com/jefferai/jsonx v1.0.1 // indirect
	github.com/jinzhu/gorm v1.9.12
	github.com/lib/pq v1.3.0
	github.com/mitchellh/mapstructure v1.1.2
	github.com/mozillazg/go-slugify v0.2.0
	github.com/mozillazg/go-unidecode v0.1.1 // indirect
	github.com/mr-tron/base58 v1.1.3
	github.com/oklog/ulid v1.3.1
	github.com/opencontainers/image-spec v1.0.1 // indirect
	github.com/opencontainers/runc v0.1.1 // indirect
	github.com/ory/dockertest v3.3.5+incompatible // indirect
	github.com/patrickmn/go-cache v2.1.0+incompatible // indirect
	github.com/pierrec/lz4 v2.0.5+incompatible
	github.com/pilagod/gorm-cursor-paginator v1.2.0 // indirect
	github.com/pkg/errors v0.9.1
	github.com/ryanuber/go-glob v1.0.0 // indirect
	github.com/seiflotfy/cuckoofilter v0.0.0-20200323075608-c8f23b6b6cef
	github.com/stretchr/testify v1.5.1
	github.com/y0ssar1an/q v1.0.10
	go.etcd.io/bbolt v1.3.3
	golang.org/x/crypto v0.0.0-20200317142112-1b76d66859c6
	golang.org/x/net v0.0.0-20200301022130-244492dfa37a
	google.golang.org/grpc v1.27.1
	gopkg.in/mgo.v2 v2.0.0-20190816093944-a6b53ec6cb22 // indirect
	gotest.tools v2.2.0+incompatible
)

replace (
	github.com/hashicorp/memberlist => ../memberlist
	github.com/hashicorp/serf => ../serf
	github.com/hashicorp/yamux => ../yamux
)

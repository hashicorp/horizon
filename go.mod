module github.com/hashicorp/horizon

go 1.13

require (
	github.com/DATA-DOG/go-txdb v0.1.3
	github.com/caddyserver/certmagic v0.10.3
	github.com/davecgh/go-spew v1.1.1
	github.com/dustinkirkland/golang-petname v0.0.0-20191129215211-8e5a1ed0cff0
	github.com/flynn/noise v0.0.0-20180327030543-2492fe189ae6
	github.com/go-sql-driver/mysql v1.5.0 // indirect
	github.com/gogo/protobuf v1.3.1
	github.com/hashicorp/go-hclog v0.12.1
	github.com/hashicorp/go-multierror v1.1.0
	github.com/hashicorp/memberlist v0.2.0
	github.com/hashicorp/serf v0.9.0
	github.com/hashicorp/yamux v0.0.0-20190923154419-df201c70410d
	github.com/jinzhu/gorm v1.9.12
	github.com/kr/pretty v0.2.0 // indirect
	github.com/lib/pq v1.3.0
	github.com/mozillazg/go-slugify v0.2.0
	github.com/mozillazg/go-unidecode v0.1.1 // indirect
	github.com/mr-tron/base58 v1.1.3
	github.com/oklog/ulid v1.3.1
	github.com/pierrec/lz4 v2.0.5+incompatible
	github.com/pkg/errors v0.9.1
	github.com/stretchr/testify v1.4.0
	go.etcd.io/bbolt v1.3.3
	golang.org/x/crypto v0.0.0-20200317142112-1b76d66859c6
	golang.org/x/net v0.0.0-20200202094626-16171245cfb2 // indirect
	gotest.tools v2.2.0+incompatible
)

replace (
	github.com/hashicorp/memberlist => ../memberlist
	github.com/hashicorp/serf => ../serf
	github.com/hashicorp/yamux => ../yamux
)

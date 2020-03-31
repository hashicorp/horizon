module github.com/hashicorp/horizon

go 1.13

require (
	github.com/DATA-DOG/go-txdb v0.1.3
	github.com/caddyserver/certmagic v0.10.3
	github.com/dustinkirkland/golang-petname v0.0.0-20191129215211-8e5a1ed0cff0
	github.com/flynn/noise v0.0.0-20180327030543-2492fe189ae6
	github.com/gogo/protobuf v1.3.1
	github.com/golang-migrate/migrate/v4 v4.10.0
	github.com/hashicorp/go-hclog v0.12.1
	github.com/hashicorp/yamux v0.0.0-20190923154419-df201c70410d
	github.com/jinzhu/gorm v1.9.12
	github.com/lib/pq v1.3.0
	github.com/mozillazg/go-slugify v0.2.0
	github.com/mozillazg/go-unidecode v0.1.1 // indirect
	github.com/mr-tron/base58 v1.1.3
	github.com/oklog/ulid v1.3.1
	github.com/pierrec/lz4 v2.0.5+incompatible
	github.com/pkg/errors v0.9.1
	github.com/sqs/goreturns v0.0.0-20181028201513-538ac6014518 // indirect
	github.com/stretchr/testify v1.4.0
	github.com/y0ssar1an/q v1.0.10
	go.etcd.io/bbolt v1.3.3
	golang.org/x/crypto v0.0.0-20200317142112-1b76d66859c6
	gotest.tools v2.2.0+incompatible
)

replace github.com/hashicorp/yamux => ../yamux

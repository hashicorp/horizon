module github.com/hashicorp/horizon

go 1.13

require (
	github.com/caddyserver/certmagic v0.10.3
	github.com/dustinkirkland/golang-petname v0.0.0-20191129215211-8e5a1ed0cff0
	github.com/flynn/noise v0.0.0-20180327030543-2492fe189ae6
	github.com/gogo/protobuf v1.3.1
	github.com/hashicorp/go-hclog v0.12.1
	github.com/hashicorp/yamux v0.0.0-20190923154419-df201c70410d
	github.com/kr/pretty v0.2.0 // indirect
	github.com/mr-tron/base58 v1.1.3
	github.com/oklog/ulid v1.3.1
	github.com/pierrec/lz4 v2.0.5+incompatible
	github.com/pkg/errors v0.8.1
	go.etcd.io/bbolt v1.3.3
	golang.org/x/crypto v0.0.0-20200317142112-1b76d66859c6
)

replace github.com/hashicorp/yamux => ../yamux

# syntax = docker/dockerfile:experimental

FROM golang:alpine AS builder

RUN apk add --no-cache git gcc libc-dev

RUN mkdir -p /tmp/hzn-prime
COPY go.sum /tmp/hzn-prime
COPY go.mod /tmp/hzn-prime

WORKDIR /tmp/hzn-prime

RUN go mod download

COPY . /tmp/hzn-src

WORKDIR /tmp/hzn-src

RUN --mount=type=cache,target=/root/.cache/go-build go build -o /tmp/hzn -ldflags "-X main.sha1ver=`git rev-parse HEAD` -X main.buildTime=$(date +'+%FT%T.%N%:z')" ./cmd/hzn

FROM alpine

COPY --from=builder /tmp/hzn /usr/bin/hzn

COPY ./pkg/control/migrations /migrations

ENTRYPOINT ["/usr/bin/hzn"]

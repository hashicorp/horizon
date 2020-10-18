## Horizon Network Service

This is the Horizon Network Service project. It provides the ability for individual agents to advertise
and connect to other agents on a hosted set of network hubs.

It is explicitly designed for operational robustness via simplicity. For instance, the hub components can
run when completely cut off from the control plane (called the central tier) and will gracefully rejoin when
the control plane is restored.

It relies on S3 for long term, persistent service routing information.


### Architectural Notes

#### Activity

There are 2 separate systems that involving passing information about events that have occured and both are
named activity.

One passes data between central tier services using postgresql, by writing records to a table
and issuing a postgresql `NOTIFY` command. This is used to flood routing updates on the control plane.
Future work on the control plane may remove this system in favor of using a format messaging service, but
for now it serves it's purpose fine and reduces the dependencies that the control plane has.

The second activity system is one used between central tier services and hubs. Hubs make a long running
gRPC bidirectional stream connection to the central tier which is picked up by one of individual running
instances. This stream is used to pass activity like routing updates but also statitics the hubs hold to
the control plane.

### Dev

To make development of the system easier, there is an explicit dev mode. First, run:

```
make dev-setup
```

This will create the PostgreSQL database, migrate it, and bring up the other services with
docker-compose.

Next, you can run a control service node AND a hub in dev mode in the same process:

```
go run ./cmd/hzn/main.go dev
```

Next, you can connect an agent to this dev hub by specifying the control address as `dev://localhost:24403`.
If you're connecting to the hub from docker, you should use `dev://docker.for.mac.localhost:24403`.

#### Ports

This setup exposes the services on the following ports:

- _24401_: Control server GRPC
- _24402_: Control server HTTP
- _24403_: Hub HZN protocol handler
- _24404_: Hub HTTP router

#### Tokens and IDs

The dev command writes 3 files that can be used to connect to them:

- _dev_agent-token.txt_: A token to connect to the hub with
- _dev-agent-id.txt_: The account ID tied to the auto created token
- _dev-mgmt-token.txt_: A management client token that can be used to configure accounts, tokens, etc.

#### HTTP Routing

The easiest way to test the HTTP routing is to just fake a Host header. Here is an example of configuring
a label-link and then sending the Hub's HTTP router a request:

```
# Run an agent that advertises an http service
$ go run ./cmd/hznagent agent --control dev://localhost:24403 --token "$(< dev-agent-token.txt)" --http 8081 --labels service=test,env=test --verbose

# Setup a label link from test.alpha.waypoint.run to the above labels
$ go run ./cmd/hznctl/main.go create-label-link --control-addr localhost:24401 --token "$(< dev-mgmt-token.txt)" --label :hostname=test.alpha.waypoint.run --account "$(< dev-agent-id.txt)" --target "service=test,env=test" --insecure

# Make a request to the HTTP routers with a Host header that matches the label link
$ curl -H "Host: test.alpha.waypoint.run" localhost:24404
```


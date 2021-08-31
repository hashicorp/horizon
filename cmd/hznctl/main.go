package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/hashicorp/horizon/pkg/grpc/lz4"
	grpctoken "github.com/hashicorp/horizon/pkg/grpc/token"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/mitchellh/cli"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var (
	sha1ver   string // sha1 revision used to build the program
	buildTime string // when the executable was built
)

func main() {
	var ver string
	if sha1ver == "" {
		ver = "unknown"
	} else {
		ver = sha1ver[:10] + "-" + buildTime
	}

	c := cli.NewCLI("hzn", ver)
	c.Args = os.Args[1:]
	c.Commands = map[string]cli.CommandFactory{
		"create-hub-token": func() (cli.Command, error) {
			return &hubTokenCreate{}, nil
		},
		"create-mgmt-token": func() (cli.Command, error) {
			return &mgmtTokenCreate{}, nil
		},
		"create-label-link": func() (cli.Command, error) {
			return &llCreate{}, nil
		},
		"create-agent-token": func() (cli.Command, error) {
			return &agentTokenCreate{}, nil
		},
	}

	exitStatus, err := c.Run()
	if err != nil {
		log.Println(err)
	}

	os.Exit(exitStatus)
}

type hubTokenCreate struct{}

func (h *hubTokenCreate) Help() string {
	return "create a hub token"
}

func (h *hubTokenCreate) Synopsis() string {
	return "create a hub token"
}

func (h *hubTokenCreate) Run(args []string) int {
	fs := pflag.NewFlagSet("hznctl", pflag.ExitOnError)

	addr := fs.String("control-addr", "127.0.0.1:24001", "Address of control server")
	insecure := fs.Bool("insecure", false, "Whether or not to secure the grpc connection")
	insecureSkipVerify := fs.Bool("insecureSkipVerify", false, "Whether or not to disable SSL verification")
	token := fs.String("token", "", "Token to authenticate with control server")

	err := fs.Parse(args)
	if err != nil {
		log.Fatal(err)
	}

	opts := []grpc.DialOption{
		grpc.WithPerRPCCredentials(grpctoken.Token(*token)),
		grpc.WithDefaultCallOptions(grpc.UseCompressor(lz4.Name)),
		grpc.WithBlock(),
	}

	opts = setTransport(opts, insecure, insecureSkipVerify)

	gcc, err := grpc.Dial(*addr, opts...)
	if err != nil {
		log.Fatalf("error dialing grpc: %s", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := pb.NewControlManagementClient(gcc)

	ctr, err := s.IssueHubToken(ctx, &pb.Noop{})
	if err != nil {
		log.Fatalf("error sending rpc: %s", err)
	}

	fmt.Println(ctr.Token)
	return 0
}

type mgmtTokenCreate struct{}

func (h *mgmtTokenCreate) Help() string {
	return "create a hub token"
}

func (h *mgmtTokenCreate) Synopsis() string {
	return "create a hub token"
}

func (h *mgmtTokenCreate) Run(args []string) int {
	fs := pflag.NewFlagSet("hznctl", pflag.ExitOnError)

	addr := fs.String("control-addr", "127.0.0.1:24001", "Address of control server")
	insecure := fs.Bool("insecure", false, "Whether or not to secure the grpc connection")
	insecureSkipVerify := fs.Bool("insecureSkipVerify", false, "Whether or not to disable SSL verification")
	token := fs.String("token", "", "Token to authenticate with control server")
	namespace := fs.String("namespace", "", "namespace to assign to this managament client")

	err := fs.Parse(args)
	if err != nil {
		log.Fatal(err)
	}

	if *namespace == "" {
		log.Fatalln("a namespace must be provided")
	}

	if *namespace == "/" {
		log.Fatalln("the root namespace is not available for use")
	}

	opts := []grpc.DialOption{
		grpc.WithPerRPCCredentials(grpctoken.Token(*token)),
		grpc.WithDefaultCallOptions(grpc.UseCompressor(lz4.Name)),
	}

	opts = setTransport(opts, insecure, insecureSkipVerify)

	gcc, err := grpc.Dial(*addr, opts...)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := pb.NewControlManagementClient(gcc)

	ctr, err := s.Register(ctx, &pb.ControlRegister{
		Namespace: *namespace,
	})

	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(ctr.Token)
	return 0
}

func setTransport(opts []grpc.DialOption, insecure *bool, insecureSkipVerify *bool) []grpc.DialOption {
	if *insecure {
		opts = append(opts, grpc.WithInsecure())
	} else {
		creds := credentials.NewTLS(&tls.Config{
			InsecureSkipVerify: *insecureSkipVerify,
		})

		opts = append(opts, grpc.WithTransportCredentials(creds))
	}
	return opts
}

type llCreate struct{}

func (h *llCreate) Help() string {
	return "Add a label link"
}

func (h *llCreate) Synopsis() string {
	return "Add a label link"
}

func (h *llCreate) Run(args []string) int {
	fs := pflag.NewFlagSet("hznctl", pflag.ExitOnError)

	addr := fs.String("control-addr", "127.0.0.1:24001", "Address of control server")
	insecure := fs.Bool("insecure", false, "Whether or not to secure the grpc connection")
	insecureSkipVerify := fs.Bool("insecureSkipVerify", false, "Whether or not to disable SSL verification")
	token := fs.String("token", "", "Token to authenticate with control server")
	gLabel := fs.String("label", "", "global label")
	acc := fs.String("account", "", "account for the label")
	namespace := fs.String("namespace", "/waypoint", "namespace to assign to this managament client")
	tLabel := fs.String("target", "", "target label")

	err := fs.Parse(args)
	if err != nil {
		log.Fatal(err)
	}

	if *gLabel == "" || *acc == "" || *tLabel == "" {
		log.Fatalln("label, target, and account must be provided")
	}

	opts := []grpc.DialOption{
		grpc.WithPerRPCCredentials(grpctoken.Token(*token)),
		grpc.WithDefaultCallOptions(grpc.UseCompressor(lz4.Name)),
	}

	opts = setTransport(opts, insecure, insecureSkipVerify)

	gcc, err := grpc.Dial(*addr, opts...)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := pb.NewControlManagementClient(gcc)

	gls := pb.ParseLabelSet(*gLabel)
	accId, err := pb.ParseULID(*acc)
	if err != nil {
		log.Fatal(err)
	}

	tls := pb.ParseLabelSet(*tLabel)

	_, err = s.AddLabelLink(ctx, &pb.AddLabelLinkRequest{
		Labels: gls,
		Account: &pb.Account{
			AccountId: accId,
			Namespace: *namespace,
		},
		Target: tls,
	})

	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Add %s => %s::%s\n", gls, accId, tls)

	return 0
}

type agentTokenCreate struct{}

func (h *agentTokenCreate) Help() string {
	return "Add an agent token"
}

func (h *agentTokenCreate) Synopsis() string {
	return "Add an agent token"
}

func (h *agentTokenCreate) Run(args []string) int {
	fs := pflag.NewFlagSet("hznctl", pflag.ExitOnError)

	addr := fs.String("control-addr", "127.0.0.1:24001", "Address of control server")
	insecure := fs.Bool("insecure", false, "Whether or not to secure the grpc connection")
	insecureSkipVerify := fs.Bool("insecureSkipVerify", false, "Whether or not to disable SSL verification")
	token := fs.String("token", "", "Token to authenticate with control server")
	acc := fs.String("account", "", "account for the label")

	err := fs.Parse(args)
	if err != nil {
		log.Fatal(err)
	}

	opts := []grpc.DialOption{
		grpc.WithPerRPCCredentials(grpctoken.Token(*token)),
		grpc.WithDefaultCallOptions(grpc.UseCompressor(lz4.Name)),
	}

	opts = setTransport(opts, insecure, insecureSkipVerify)

	gcc, err := grpc.Dial(*addr, opts...)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := pb.NewControlManagementClient(gcc)

	accId, err := pb.ParseULID(*acc)
	if err != nil {
		log.Fatal(err)
	}

	ctr, err := s.CreateToken(ctx, &pb.CreateTokenRequest{
		Account: &pb.Account{
			AccountId: accId,
			Namespace: "/waypoint",
		},
		Capabilities: []pb.TokenCapability{
			{
				Capability: pb.SERVE,
			},
			{
				Capability: pb.CONNECT,
			},
		},
	})

	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(ctr.Token)

	return 0
}

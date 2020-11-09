package main

import (
	"context"
	"flag"
	"time"

	"github.com/karimra/srl-ndk-demo/agent"
	"github.com/openconfig/gnmi/proto/gnmi"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const (
	retryInterval        = 2 * time.Second
	agentName            = "prometheus-exporter"
	defaultMetricHelp    = "SRLinux generated metric"
	gnmiServerUnixSocket = "unix:///opt/srlinux/var/run/sr_gnmi_server"
)

func main() {
	debug := flag.Bool("d", false, "turn on debug")
	flag.Parse()

	log.SetFormatter(&log.TextFormatter{
		DisableColors: true,
		FullTimestamp: true,
	})
	log.SetReportCaller(true)

	log.SetLevel(log.InfoLevel)
	if *debug {
		log.SetLevel(log.DebugLevel)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "agent_name", agentName)
CRAGENT:
	app, err := agent.NewAgent(ctx, agentName)
	if err != nil {
		log.Errorf("failed to create agent '%s': %v", agentName, err)
		log.Infof("retrying in %s", retryInterval)
		time.Sleep(retryInterval)
		goto CRAGENT
	}
	cfg := newconfig()
	log.Infof("starting with default cfg: %+v", cfg)
	exporter := newServer(WithAgent(app), WithConfig(cfg))

	log.Infof("starting config handler...")
	exporter.configHandler(ctx)
}

func createGNMIClient(ctx context.Context) (gnmi.GNMIClient, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, retryInterval)
	defer cancel()
	conn, err := grpc.DialContext(timeoutCtx, gnmiServerUnixSocket, grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		return nil, err
	}
	return gnmi.NewGNMIClient(conn), nil
}

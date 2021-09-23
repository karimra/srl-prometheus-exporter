package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	"github.com/karimra/srl-ndk-demo/agent"
	"github.com/openconfig/gnmi/proto/gnmi"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gopkg.in/yaml.v2"
)

const (
	retryInterval         = 2 * time.Second
	agentName             = "prometheus-exporter"
	defaultMetricHelp     = "SRLinux generated metric"
	gnmiServerUnixSocket  = "unix:///opt/srlinux/var/run/sr_gnmi_server"
	defaultConfigFileName = "./metrics.yaml"
)

type fileConfig struct {
	Metrics  map[string][]string `yaml:"metrics,omitempty"`
	Username string              `yaml:"username,omitempty"`
	Password string              `yaml:"password,omitempty"`
}

var version = "dev"

func main() {
	cfgFile := flag.String("c", defaultConfigFileName, "configuration file")
	debug := flag.Bool("d", false, "turn on debug")
	versionFlag := flag.Bool("v", false, "print version")
	flag.Parse()

	if *versionFlag {
		fmt.Println(version)
		return
	}
	if *debug {
		log.SetLevel(log.DebugLevel)
		log.SetReportCaller(true)
	}

READFILE:
	var fc fileConfig
	_, err := os.Stat(*cfgFile)
	if err == nil {
		b, err := ioutil.ReadFile(*cfgFile)
		if err != nil {
			log.Errorf("failed to read the configuration file: %v", err)
			time.Sleep(retryInterval)
			goto READFILE
		}
		err = yaml.Unmarshal(b, &fc)
		if err != nil {
			log.Errorf("failed to unmarshal the configuration file: %v", err)
			time.Sleep(retryInterval)
			goto READFILE
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "agent_name", agentName)

CRAGENT:
	app, err := agent.NewAgent(ctx, agentName)
	if err != nil {
		log.Errorf("failed to create agent %q: %v", agentName, err)
		log.Infof("retrying in %s", retryInterval)
		time.Sleep(retryInterval)
		goto CRAGENT
	}

	cfg := newconfig(fc)
	log.Infof("starting with default configuration: %+v", cfg)
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

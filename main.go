package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	agent "github.com/karimra/srl-ndk-demo"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"google.golang.org/grpc/metadata"
	"gopkg.in/yaml.v2"

	"github.com/karimra/srl-prometheus-exporter/app"
)

const (
	retryInterval         = 2 * time.Second
	maxRetries            = 50
	agentName             = "prometheus-exporter"
	defaultConfigFileName = "/opt/prometheus-exporter/metrics.yaml"
)

var version = "dev"

// flags
var debug bool
var cfgFile string
var versionFlag bool

func main() {
	pflag.StringVarP(&cfgFile, "config", "c", defaultConfigFileName, "configuration file")
	pflag.BoolVarP(&debug, "debug", "d", false, "turn on debug")
	pflag.BoolVarP(&versionFlag, "version", "v", false, "print version")
	pflag.Parse()

	if versionFlag {
		fmt.Println(version)
		return
	}
	if debug {
		log.SetLevel(log.DebugLevel)
		log.SetReportCaller(true)
	}
	retryCount := 0
READFILE:
	var fc = new(app.FileConfig)
	_, err := os.Stat(cfgFile)
	if err == nil {
		b, err := os.ReadFile(cfgFile)
		if err != nil {
			if retryCount >= maxRetries {
				log.Errorf("failed to read file: max retries reached: %v", err)
				os.Exit(1)
			}
			retryCount++
			log.Errorf("failed to read the configuration file: %v", err)
			time.Sleep(retryInterval)
			goto READFILE
		}
		err = yaml.Unmarshal(b, fc)
		if err != nil {
			if retryCount >= maxRetries {
				log.Errorf("failed to read file: max retries reached: %v", err)
				os.Exit(1)
			}
			log.Errorf("failed to unmarshal the configuration file: %v", err)
			time.Sleep(retryInterval)
			goto READFILE
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	setupCloseHandler(cancel)

	ctx = metadata.AppendToOutgoingContext(ctx, "agent_name", agentName)

	retryCount = 0
CRAGENT:
	agt, err := agent.New(ctx, agentName)
	if err != nil {
		if retryCount >= maxRetries {
			log.Errorf("ailed to create agent: max retries reached: %v", err)
			os.Exit(1)
		}
		log.Errorf("failed to create agent %q: %v", agentName, err)
		log.Infof("retrying in %s", retryInterval)
		time.Sleep(retryInterval)
		goto CRAGENT
	}

	cfg := app.NewConfig(fc, agentName, debug)
	log.Infof("starting with default configuration: %+v", cfg)
	server := app.NewServer(
		app.WithAgent(agt),
		app.WithConfig(cfg),
	)

	log.Infof("starting config handler...")
	server.ConfigHandler(ctx)
}

func setupCloseHandler(cancelFn context.CancelFunc) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-c
		log.Printf("received signal '%s'. terminating...", sig.String())
		cancelFn()
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()
}

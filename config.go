package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"google.golang.org/protobuf/encoding/prototext"

	ndk "github.com/karimra/go-srl-ndk"
)

const (
	operUp       = "OPER_STATE_up"
	operDown     = "OPER_STATE_down"
	operStarting = "OPER_STATE_starting"
	operFailed   = "OPER_STATE_failed"

	adminEnable  = "ADMIN_STATE_enable"
	adminDisable = "ADMIN_STATE_disable"

	stateEnable  = "STATE_enable"
	stateDisable = "STATE_disable"

	exporterPath     = ".system.prometheus_exporter"
	metricPath       = ".system.prometheus_exporter.metric"
	customMetricPath = ".system.prometheus_exporter.custom_metric"
)

type config struct {
	baseConfig *baseConfig

	m            *sync.Mutex
	trx          []*ndk.ConfigNotification
	nwInst       map[string]*ndk.NetworkInstanceData
	metrics      map[string]*metricConfig
	customMetric map[string]*customMetric
}

func newconfig() *config {
	kmetrics := make(map[string]*metricConfig)
	for n := range knownMetrics {
		kmetrics[n] = &metricConfig{}
		kmetrics[n].Metric.State = stateDisable
	}
	bcfg := &baseConfig{
		AdminState: adminDisable,
		OperState:  operDown,
	}

	return &config{
		baseConfig:   bcfg,
		m:            new(sync.Mutex),
		nwInst:       make(map[string]*ndk.NetworkInstanceData),
		metrics:      kmetrics,
		customMetric: make(map[string]*customMetric),
	}
}

type baseConfig struct {
	AdminState      string `json:"admin_state,omitempty"`
	OperState       string `json:"oper_state,omitempty"`
	NetworkInstance struct {
		Value string `json:"value,omitempty"`
	} `json:"network_instance,omitempty"`
	Address struct {
		Value string `json:"value,omitempty"`
	} `json:"address,omitempty"`
	Port struct {
		Value string `json:"value,omitempty"`
	} `json:"port,omitempty"`
	HttpPath struct {
		Value string `json:"value,omitempty"`
	} `json:"http_path,omitempty"`
	ScrapesCount struct {
		Value uint64 `json:"value,omitempty"`
	} `json:"scrapes_count,omitempty"`
}

type metricConfig struct {
	Metric struct {
		State string `json:"state,omitempty"`
	} `json:"metric,omitempty"`
}

type customMetric struct {
	CustomMetric struct {
		Paths []struct {
			Value string `json:"value,omitempty"`
		} `json:"paths,omitempty"`
		State string `json:"state,omitempty"`
	} `json:"custom_metric,omitempty"`
}

func (s *server) configHandler(ctx context.Context) {
	cfgStream := s.agent.StartConfigNotificationStream(ctx)
	nwInstStream := s.agent.StartNwInstNotificationStream(ctx)
	for {
		select {
		case nwInstEvent := <-nwInstStream:
			log.Debugf("NwInst notification: %+v", nwInstEvent)
			b, err := prototext.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(nwInstEvent)
			if err != nil {
				log.Errorf("NwInst notification Marshal failed: %+v", err)
				continue
			}
			fmt.Printf("%s\n", string(b))
			for _, ev := range nwInstEvent.GetNotification() {
				if nwInst := ev.GetNwInst(); nwInst != nil {
					s.handleNwInstCfg(ctx, nwInst)
					continue
				}
				log.Warnf("got empty nwInst, event: %+v", ev)
			}
		case event := <-cfgStream:
			log.Infof("Config notification: %+v", event)
			b, err := prototext.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(event)
			if err != nil {
				log.Infof("Config notification Marshal failed: %+v", err)
				continue
			}
			fmt.Printf("%s\n", string(b))

			for _, ev := range event.GetNotification() {
				if cfg := ev.GetConfig(); cfg != nil {
					s.handleConfigEvent(ctx, cfg)
					continue
				}
				log.Infof("got empty config, event: %+v", ev)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *server) handleConfigEvent(ctx context.Context, cfg *ndk.ConfigNotification) {
	log.Infof("handling cfg: %+v", cfg)

	// collect non commit.end config notifications
	if cfg.Key.JsPath != ".commit.end" {
		s.config.trx = append(s.config.trx, cfg)
		return
	}
	// when paths is "".commit.end", handle the stored config notifications
	for _, cfg := range s.config.trx {
		switch cfg.Key.JsPath {
		case exporterPath:
			switch cfg.Op {
			case ndk.SdkMgrOperation_Create:
				s.handleCfgPrometheusCreate(ctx, cfg)
			case ndk.SdkMgrOperation_Change:
				s.handleCfgPrometheusChange(ctx, cfg)
			case ndk.SdkMgrOperation_Delete:
				log.Infof("received delete Operation for path '.prometheus_exporter', this is unexpected...")
			}
		case metricPath:
			if len(cfg.Key.Keys) == 0 {
				log.Infof("'%s' no keys in cfg notification: %+v", metricPath, cfg)
				return
			}
			switch cfg.Op {
			case ndk.SdkMgrOperation_Create:
				s.handleCfgMetricCreate(ctx, cfg)
			case ndk.SdkMgrOperation_Change:
				s.handleCfgMetricChange(ctx, cfg)
			case ndk.SdkMgrOperation_Delete:
				s.handleCfgMetricDelete(ctx, cfg)
			}
		case customMetricPath:
			if len(cfg.Key.Keys) == 0 {
				log.Infof("'%s' no keys in cfg notification: %+v", customMetricPath, cfg)
				return
			}
			switch cfg.Op {
			case ndk.SdkMgrOperation_Change:
				s.handleCfgCustomMetricCreateChange(ctx, cfg)
			case ndk.SdkMgrOperation_Create:
				s.handleCfgCustomMetricCreateChange(ctx, cfg)
			case ndk.SdkMgrOperation_Delete:
				s.handleCfgCustomMetricDelete(ctx, cfg)
			}
		}
	}
	s.config.m.Lock()
	s.config.trx = make([]*ndk.ConfigNotification, 0)
	s.config.m.Unlock()
}

func (s *server) handleCfgPrometheusCreate(ctx context.Context, cfg *ndk.ConfigNotification) {
	newCfg := new(baseConfig)
	err := json.Unmarshal([]byte(cfg.GetData().GetJson()), newCfg)
	if err != nil {
		log.Infof("failed to marshal config data from path '%s': %v", cfg.GetKey().GetJsPath(), err)
		return
	}
	log.Infof("read base config data: %+v", newCfg)

	s.config.m.Lock()
	defer s.config.m.Unlock()

	// set default oper state
	newCfg.OperState = operDown
	// store initial config
	s.config.baseConfig = newCfg

	// start server if admin-state == enable
	if newCfg.AdminState == adminEnable && s.config.baseConfig.OperState != operStarting {
		newCfg.OperState = operStarting
		s.updatePrometheusBaseTelemetry(ctx, newCfg)
		go s.start(ctx)
		return
	}
	// update internal telemetry status
	s.updatePrometheusBaseTelemetry(ctx, newCfg)
}

func (s *server) handleCfgPrometheusChange(ctx context.Context, cfg *ndk.ConfigNotification) {
	newCfg := new(baseConfig)
	err := json.Unmarshal([]byte(cfg.GetData().GetJson()), newCfg)
	if err != nil {
		log.Infof("failed to marshal config data from path '%s': %v", cfg.GetKey().GetJsPath(), err)
		return
	}
	log.Infof("read base config data: %+v", newCfg)

	s.config.m.Lock()
	defer s.config.m.Unlock()

	// save current oper state
	newCfg.OperState = s.config.baseConfig.OperState
	// store new config
	s.config.baseConfig = newCfg
	log.Infof("new stored config: %+v", s.config.baseConfig)
	if newCfg.AdminState == adminDisable && s.config.baseConfig.OperState != operDown {
		// shutdown the http server with a 1s timeout
		s.shutdown(ctx, time.Second)
	} else if (newCfg.AdminState == adminEnable && s.config.baseConfig.OperState == operDown)  {
		// start http server
		go s.start(ctx)
	}
	// update internal telemetry status
	s.updatePrometheusBaseTelemetry(ctx, newCfg)
}

func (s *server) handleCfgMetricCreate(ctx context.Context, cfg *ndk.ConfigNotification) {
	key := cfg.Key.Keys[0]
	newMetricConfig := new(metricConfig)
	err := json.Unmarshal([]byte(cfg.GetData().GetJson()), newMetricConfig)
	if err != nil {
		log.Infof("failed to marshal config data from path %s: %v", cfg.Key.JsPath, err)
		return
	}
	log.Infof("read metric config data: %+v", newMetricConfig)

	s.config.m.Lock()
	defer s.config.m.Unlock()
	if _, ok := s.config.metrics[key]; !ok {
		s.config.metrics[key] = new(metricConfig)
	}

	// store new config
	s.config.metrics[key] = newMetricConfig
	// update metric telemetry
	s.updateMetricTelemetry(ctx, key, newMetricConfig)
}

func (s *server) handleCfgMetricChange(ctx context.Context, cfg *ndk.ConfigNotification) {
	key := cfg.Key.Keys[0]
	newMetricConfig := new(metricConfig)
	err := json.Unmarshal([]byte(cfg.GetData().GetJson()), newMetricConfig)
	if err != nil {
		log.Infof("failed to marshal config data from path %s: %v", cfg.Key.JsPath, err)
		return
	}
	s.config.m.Lock()
	defer s.config.m.Unlock()

	// store new config
	s.config.metrics[key].Metric.State = newMetricConfig.Metric.State
	// update metric telemetry
	s.updateMetricTelemetry(ctx, key, newMetricConfig)
}

func (s *server) handleCfgMetricDelete(ctx context.Context, cfg *ndk.ConfigNotification) {
	key := cfg.Key.Keys[0]
	s.config.m.Lock()
	defer s.config.m.Unlock()
	if _, ok := s.config.metrics[key]; !ok {
		log.Errorf("Op delete metric, cannot find metric '%s'", key)
		return
	}
	s.config.metrics[key] = &metricConfig{}
	s.config.metrics[key].Metric.State = stateDisable
	s.deleteMetricTelemetry(ctx, key)
}

func (s *server) handleCfgCustomMetricCreateChange(ctx context.Context, cfg *ndk.ConfigNotification) {
	key := cfg.Key.Keys[0]
	newMetricConfig := new(customMetric)
	err := json.Unmarshal([]byte(cfg.GetData().GetJson()), newMetricConfig)
	if err != nil {
		log.Infof("failed to marshal config data from path %s: %v", cfg.Key.JsPath, err)
		return
	}
	log.Infof("read metric config data: %+v", newMetricConfig)

	s.config.m.Lock()
	defer s.config.m.Unlock()
	if _, ok := s.config.customMetric[key]; !ok {
		s.config.customMetric[key] = new(customMetric)
	}

	// store new config
	s.config.customMetric[key] = newMetricConfig
	// update metric telemetry
	s.updateCustomMetricTelemetry(ctx, key, newMetricConfig)
}

func (s *server) handleCfgCustomMetricDelete(ctx context.Context, cfg *ndk.ConfigNotification) {
	key := cfg.Key.Keys[0]
	s.config.m.Lock()
	defer s.config.m.Unlock()
	if _, ok := s.config.customMetric[key]; !ok {
		log.Errorf("Op delete custom metric, cannot find custom metric '%s'", key)
		return
	}
	delete(s.config.customMetric, key)
	s.deleteCustomMetricTelemetry(ctx, key)
}

func (s *server) handleNwInstCfg(ctx context.Context, nwInst *ndk.NetworkInstanceNotification) {
	key := nwInst.GetKey()
	if key == nil {
		return
	}
	s.config.m.Lock()
	defer s.config.m.Unlock()
	switch nwInst.Op {
	case ndk.SdkMgrOperation_Create:
		s.config.nwInst[key.InstName] = nwInst.Data
		if s.config.baseConfig.NetworkInstance.Value == nwInst.Key.InstName {
			if nwInst.Data.OperIsUp && s.config.baseConfig.AdminState == adminEnable {
				go s.start(ctx)
			}
		}
	case ndk.SdkMgrOperation_Change:
		s.config.nwInst[key.InstName] = nwInst.Data
		if s.config.baseConfig.NetworkInstance.Value == nwInst.Key.InstName {
			if !nwInst.Data.OperIsUp {
				if s.config.baseConfig.OperState == operUp {
					s.shutdown(ctx, time.Second)
				}
			} else {
				if s.config.baseConfig.AdminState == adminEnable && s.config.baseConfig.OperState == operDown {
					go s.start(ctx)
				}
			}
		}
	case ndk.SdkMgrOperation_Delete:
		delete(s.config.nwInst, key.InstName)
		if s.config.baseConfig.NetworkInstance.Value == nwInst.Key.InstName {
			if s.config.baseConfig.OperState == operUp {
				s.shutdown(ctx, time.Second)
			}
		}
	}
}

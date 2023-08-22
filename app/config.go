package app

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/nokia/srlinux-ndk-go/ndk"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/prototext"
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

type stringValue struct {
	Value string `json:"value,omitempty"`
}

type uint64Value struct {
	Value uint64 `json:"value,omitempty"`
}

type boolValue struct {
	Value bool `json:"value,omitempty"`
}

type config struct {
	agentName string

	baseConfig *baseConfig

	m            *sync.Mutex
	trx          []*ndk.ConfigNotification
	nwInst       map[string]*ndk.NetworkInstanceData
	metrics      map[string]*metricConfig
	customMetric map[string]*customMetricConfig

	// from file
	username string
	password string
	//
	debug bool
}

type FileConfig struct {
	Metrics  map[string][]string `yaml:"metrics,omitempty"`
	Username string              `yaml:"username,omitempty"`
	Password string              `yaml:"password,omitempty"`
}

func NewConfig(fc *FileConfig, agentName string, debug bool) *config {
	if len(fc.Metrics) > 0 {
		knownMetrics = fc.Metrics
	}

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
		agentName:    agentName,
		baseConfig:   bcfg,
		m:            new(sync.Mutex),
		nwInst:       make(map[string]*ndk.NetworkInstanceData),
		metrics:      kmetrics,
		customMetric: make(map[string]*customMetricConfig),
		username:     fc.Username,
		password:     fc.Password,
		debug:        debug,
	}
}

type baseConfig struct {
	AdminState      string        `json:"admin_state,omitempty"`
	OperState       string        `json:"oper_state,omitempty"`
	NetworkInstance stringValue   `json:"network_instance,omitempty"`
	Address         stringValue   `json:"address,omitempty"`
	Port            stringValue   `json:"port,omitempty"`
	HttpPath        stringValue   `json:"http_path,omitempty"`
	ScrapesCount    uint64Value   `json:"scrapes_count,omitempty"`
	Registration    *registration `json:"registration,omitempty"`
}

type metricConfig struct {
	Metric metric `json:"metric,omitempty"`
}

type customMetricConfig struct {
	Metric metric `json:"custom_metric,omitempty"`
}

type metric struct {
	State    string        `json:"state,omitempty"`
	HelpText stringValue   `json:"help_text,omitempty"`
	Paths    []stringValue `json:"paths,omitempty"`
}

type registration struct {
	Address    stringValue   `json:"address,omitempty"`
	Username   stringValue   `json:"username,omitempty"`
	Password   stringValue   `json:"password,omitempty"`
	Token      stringValue   `json:"token,omitempty"`
	TTL        stringValue   `json:"ttl,omitempty"`
	HTTPCheck  boolValue     `json:"http-check,omitempty"`
	Tags       []stringValue `json:"tags,omitempty"`
	AdminState string        `json:"admin_state,omitempty"`
	OperState  string        `json:"oper_state,omitempty"`
}

func (s *server) ConfigHandler(ctx context.Context) {
	cfgStream := s.agent.StartConfigNotificationStream(ctx)
	nwInstStream := s.agent.StartNwInstNotificationStream(ctx)
	for {
		select {
		case nwInstEvent := <-nwInstStream:
			if s.config.debug {
				log.Debugf("NwInst notification: %+v", nwInstEvent)
				b, err := prototext.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(nwInstEvent)
				if err != nil {
					log.Errorf("NwInst notification Marshal failed: %+v", err)
					continue
				}
				log.Debugf("%s\n", string(b))
			}
			for _, ev := range nwInstEvent.GetNotification() {
				if nwInst := ev.GetNwInst(); nwInst != nil {
					s.handleNwInstCfg(ctx, nwInst)
					continue
				}
				log.Warnf("got empty nwInst, event: %+v", ev)
			}
		case event := <-cfgStream:
			if s.config.debug {
				log.Debugf("Config notification: %+v", event)
				b, err := prototext.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(event)
				if err != nil {
					log.Errorf("Config notification Marshal failed: %+v", err)
					continue
				}
				log.Debugf("%s\n", string(b))
			}
			for _, ev := range event.GetNotification() {
				if cfg := ev.GetConfig(); cfg != nil {
					s.handleConfigEvent(ctx, cfg)
					continue
				}
				log.Warnf("got empty config, event: %+v", ev)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *server) handleConfigEvent(ctx context.Context, cfg *ndk.ConfigNotification) {
	log.Debugf("handling cfg: %+v", cfg)
	log.Debugf("PATH: %s\n", cfg.GetKey().GetJsPath())
	log.Debugf("KEYS: %v\n", cfg.GetKey().GetKeys())
	log.Debugf("JSON:\n%s\n", cfg.GetData().GetJson())

	jsPath := cfg.GetKey().GetJsPath()
	// collect non commit.end config notifications
	if jsPath != ".commit.end" {
		s.config.trx = append(s.config.trx, cfg)
		return
	}
	// when paths is ".commit.end", handle the stored config notifications
	for _, txCfg := range s.config.trx {
		switch txCfg.GetKey().GetJsPath() {
		case exporterPath:
			switch txCfg.Op {
			case ndk.SdkMgrOperation_Create:
				s.handleCfgPrometheusCreate(ctx, txCfg)
			case ndk.SdkMgrOperation_Update:
				s.handleCfgPrometheusChange(ctx, txCfg)
			case ndk.SdkMgrOperation_Delete:
				log.Errorf("received delete Operation for path %q, this is unexpected...", exporterPath)
			}
		case metricPath:
			if len(txCfg.GetKey().GetKeys()) == 0 {
				log.Errorf("%q no keys in cfg notification: %+v", metricPath, txCfg)
				return
			}
			switch txCfg.Op {
			case ndk.SdkMgrOperation_Create:
				s.handleCfgMetricCreate(ctx, txCfg)
			case ndk.SdkMgrOperation_Update:
				s.handleCfgMetricChange(ctx, txCfg)
			case ndk.SdkMgrOperation_Delete:
				s.handleCfgMetricDelete(ctx, txCfg)
			}
		case customMetricPath:
			if len(txCfg.Key.Keys) == 0 {
				log.Errorf("%q no keys in cfg notification: %+v", customMetricPath, txCfg)
				return
			}
			switch txCfg.Op {
			case ndk.SdkMgrOperation_Update:
				s.handleCfgCustomMetricCreateChange(ctx, txCfg)
			case ndk.SdkMgrOperation_Create:
				s.handleCfgCustomMetricCreateChange(ctx, txCfg)
			case ndk.SdkMgrOperation_Delete:
				s.handleCfgCustomMetricDelete(ctx, txCfg)
			}
		default:
			log.Errorf("unexpected config path %q", txCfg.GetKey().GetJsPath())
		}
	}
	s.config.m.Lock()
	s.config.trx = make([]*ndk.ConfigNotification, 0)
	s.config.m.Unlock()
}

func (s *server) handleCfgPrometheusCreate(ctx context.Context, cfg *ndk.ConfigNotification) {
	newCfg := &baseConfig{
		Registration: &registration{
			AdminState: adminDisable,
			OperState:  operDown,
		},
	}
	err := json.Unmarshal([]byte(cfg.GetData().GetJson()), newCfg)
	if err != nil {
		log.Errorf("failed to marshal config data from path %q: %v", cfg.GetKey().GetJsPath(), err)
		return
	}
	b, err := json.MarshalIndent(newCfg, "", "  ")
	if err != nil {
		log.Errorf("failed to Marshal baseconfig: %v", err)
		return
	}
	log.Debugf("read baseconfig data: %s", string(b))

	s.config.m.Lock()
	defer s.config.m.Unlock()

	// set default oper state
	newCfg.OperState = operDown
	// store initial config
	s.config.baseConfig = newCfg

	// start server if admin-state == enable
	if newCfg.AdminState == adminEnable && s.config.baseConfig.OperState != operStarting {
		newCfg.OperState = operStarting
		go s.start(ctx)
	}
	// update internal telemetry status
	s.updatePrometheusBaseTelemetry(ctx, newCfg)
}

func (s *server) handleCfgPrometheusChange(ctx context.Context, cfg *ndk.ConfigNotification) {
	newCfg := &baseConfig{
		Registration: new(registration),
	}
	err := json.Unmarshal([]byte(cfg.GetData().GetJson()), newCfg)
	if err != nil {
		log.Errorf("failed to marshal config data from path %q: %v", cfg.GetKey().GetJsPath(), err)
		return
	}
	b, err := json.MarshalIndent(newCfg, "", "  ")
	if err != nil {
		log.Errorf("failed to Marshal baseconfig: %v", err)
		return
	}
	log.Debugf("read baseconfig data: %s", string(b))

	s.config.m.Lock()
	defer s.config.m.Unlock()

	if newCfg.AdminState == adminDisable && s.config.baseConfig.OperState != operDown {
		// shutdown the http server with a 1s timeout
		s.shutdown(ctx, time.Second)
		return
	}
	if newCfg.AdminState == adminEnable && s.config.baseConfig.OperState == operDown {
		// start http server
		go s.start(ctx)
		return
	}
	// HTTP server already running, check if registration has to be started or stopped
	if s.config.baseConfig.OperState == operUp && s.config.baseConfig.AdminState == adminEnable {
		log.Debug("server is up, checking if registration needs to be started...")
		// server is already up, check if registration needs to be started
		if newCfg.Registration.AdminState == adminEnable && s.config.baseConfig.Registration.OperState == operDown {
			go s.registerService(ctx)
		} else if newCfg.Registration.AdminState == adminDisable && s.config.baseConfig.OperState != operUp {
			if s.regCancelFn != nil {
				s.regCancelFn()
			}
		}
	}

	// save current oper state
	newCfg.OperState = s.config.baseConfig.OperState
	newCfg.Registration.OperState = s.config.baseConfig.Registration.OperState
	// store new config
	s.config.baseConfig = newCfg
	// update internal telemetry status
	s.updatePrometheusBaseTelemetry(ctx, s.config.baseConfig)
}

func (s *server) handleCfgMetricCreate(ctx context.Context, cfg *ndk.ConfigNotification) {
	key := cfg.Key.Keys[0]
	newMetricConfig := new(metricConfig)
	err := json.Unmarshal([]byte(cfg.GetData().GetJson()), newMetricConfig)
	if err != nil {
		log.Errorf("failed to marshal config data from path %s: %v", cfg.Key.JsPath, err)
		return
	}
	log.Debugf("read metric config data: %+v", newMetricConfig)

	s.config.m.Lock()
	defer s.config.m.Unlock()
	if _, ok := s.config.metrics[key]; !ok {
		s.config.metrics[key] = new(metricConfig)
	}
	log.Debugf("looking for known metrics with key : %s", key)
	if knownMetricPaths, ok := knownMetrics[key]; ok {
		log.Debugf("found known metric paths: %+v", knownMetricPaths)
		newMetricConfig.Metric.Paths = make([]stringValue, len(knownMetricPaths))
		for i, p := range knownMetricPaths {
			newMetricConfig.Metric.Paths[i].Value = p
		}
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
		log.Errorf("failed to marshal config data from path %s: %v", cfg.Key.JsPath, err)
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
		log.Errorf("Op delete metric, cannot find metric %q", key)
		return
	}
	s.config.metrics[key] = &metricConfig{}
	s.config.metrics[key].Metric.State = stateDisable
	s.deleteMetricTelemetry(ctx, key)
}

func (s *server) handleCfgCustomMetricCreateChange(ctx context.Context, cfg *ndk.ConfigNotification) {
	key := cfg.Key.Keys[0]
	newMetricConfig := new(customMetricConfig)
	err := json.Unmarshal([]byte(cfg.GetData().GetJson()), newMetricConfig)
	if err != nil {
		log.Errorf("failed to marshal config data from path %s: %v", cfg.Key.JsPath, err)
		return
	}
	log.Debugf("read metric config data: %+v", newMetricConfig)

	s.config.m.Lock()
	defer s.config.m.Unlock()
	if _, ok := s.config.customMetric[key]; !ok {
		s.config.customMetric[key] = new(customMetricConfig)
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
		log.Errorf("Op delete custom metric, cannot find custom metric %q", key)
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
			if nwInst.Data.OperIsUp &&
				s.config.baseConfig.AdminState == adminEnable &&
				s.config.baseConfig.OperState == operDown {
				go s.start(ctx)
			}
		}
	case ndk.SdkMgrOperation_Update:
		s.config.nwInst[key.InstName] = nwInst.Data
		if s.config.baseConfig.NetworkInstance.Value == nwInst.Key.InstName {
			if !nwInst.Data.OperIsUp {
				if s.config.baseConfig.OperState == operUp {
					s.shutdown(ctx, time.Second)
				}
				return
			}
			if s.config.baseConfig.AdminState == adminEnable &&
				s.config.baseConfig.OperState == operDown {
				go s.start(ctx)
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

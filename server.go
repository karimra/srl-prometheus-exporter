package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	capi "github.com/hashicorp/consul/api"
	agent "github.com/karimra/srl-ndk-demo"
	"github.com/openconfig/gnmi/proto/gnmi"
	"github.com/openconfig/gnmic/formatters"
	"github.com/openconfig/gnmic/utils"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netns"
	"google.golang.org/grpc/metadata"
)

const (
	metricNameRegex = "[^a-zA-Z0-9_]+"
	serviceName     = "srl-prometheus-exporter"
)

var sysInfoPaths = []*gnmi.Path{
	{
		Elem: []*gnmi.PathElem{
			{Name: "system"},
			{Name: "name"},
			{Name: "host-name"},
		},
	},
	{
		Elem: []*gnmi.PathElem{
			{Name: "interface",
				Key: map[string]string{"name": "mgmt0"},
			},
			{Name: "subinterface"},
			{Name: "ipv4"},
			{Name: "address"},
			{Name: "status"},
		},
	},
	{
		Elem: []*gnmi.PathElem{
			{Name: "interface",
				Key: map[string]string{"name": "mgmt0"},
			},
			{Name: "subinterface"},
			{Name: "ipv6"},
			{Name: "address"},
			{Name: "status"},
		},
	},
	{
		Elem: []*gnmi.PathElem{
			{Name: "system"},
			{Name: "information"},
			{Name: "version"},
		},
	},
	{
		Elem: []*gnmi.PathElem{
			{Name: "platform"},
			{Name: "chassis"},
		},
	},
}

type server struct {
	config *config
	agent  *agent.Agent

	srv         *http.Server
	srvCancelFn context.CancelFunc
	regCancelFn context.CancelFunc
	//
	consulClient *capi.Client
	metricRegex  *regexp.Regexp
}

type serverOption func(*server)

func WithAgent(agt *agent.Agent) func(s *server) {
	return func(s *server) {
		s.agent = agt
	}
}

func WithConfig(c *config) func(s *server) {
	return func(s *server) {
		s.config = c
	}
}

type systemInfo struct {
	Name                string
	Version             string
	ChassisType         string
	ChassisMacAddress   string
	ChassisCLEICode     string
	ChassisPartNumber   string
	ChassisSerialNumber string
	NetworkInstance     string
	IPAddrV4            string
	IPAddrV6            string
}

// Describe implements prometheus.Collector
func (s *server) Describe(ch chan<- *prometheus.Desc) {}

// Collect implements prometheus.Collector
func (s *server) Collect(ch chan<- prometheus.Metric) {
	atomic.AddUint64(&s.config.baseConfig.ScrapesCount.Value, 1)
	statsCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	statsCtx = metadata.AppendToOutgoingContext(statsCtx, "agent_name", agentName)
	s.updatePrometheusBaseTelemetry(statsCtx, s.config.baseConfig)

	gctx, gcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer gcancel()

	conn, gnmiClient, err := createGNMIClient(gctx)
	if err != nil {
		log.Infof("failed to create a gnmi connection to %q: %v", gnmiServerUnixSocket, err)
		return
	}
	defer conn.Close()

	// lock config
	s.config.m.Lock()
	defer s.config.m.Unlock()

	// get metrics that are enabled
	metrics := make(map[string]metric, len(s.config.metrics)+len(s.config.customMetric))
	for name, m := range s.config.metrics {
		if m.Metric.State == stateEnable {
			metrics[name] = m.Metric
		}
	}
	for name, m := range s.config.customMetric {
		if m.Metric.State == stateEnable {
			metrics[name] = m.Metric
		}
	}

	log.Debugf("about to collect metrics: %+v", metrics)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// add credentials to context
	ctx = metadata.AppendToOutgoingContext(ctx, "username", s.config.username, "password", s.config.password)

	wg := new(sync.WaitGroup)
	wg.Add(len(metrics))
	for name, m := range metrics {
		go func(name string, m metric) {
			defer wg.Done()
			log.Debugf("collecting metric %q", name)
			req, err := s.createSubscribeRequest(name)
			if err != nil {
				return
			}

			sctx, cancel := context.WithCancel(ctx)
			defer cancel()
			subClient, err := gnmiClient.Subscribe(sctx)
			if err != nil {
				log.Errorf("failed to create a subscribe client for metric %q: %v", name, err)
				return
			}
			defer subClient.CloseSend()

			log.Debugf("sending subscribe request: %+v", req)
			err = subClient.Send(req)
			if err != nil {
				log.Errorf("failed to send a subscribe request for metric %q: %v", name, err)
				return
			}
			for {
				subResp, err := subClient.Recv()
				if err == io.EOF {
					log.Infof("subscription for metric %q received EOF, subscription done", name)
					return
				}
				if err != nil {
					log.Errorf("failed to receive a subscribe request for metric %q: %v", name, err)
					return
				}

				log.Debugf("received subscribe response: %+v", subResp)
				events, err := formatters.ResponseToEventMsgs("", subResp, nil)
				if err != nil {
					log.Errorf("failed to convert message to event: %v", err)
					return
				}
				for _, ev := range events {
					labels, values := s.getLabels(ev)
					for vname, v := range ev.Values {
						v, err := getFloat(v)
						if err != nil {
							continue
						}
						ch <- prometheus.MustNewConstMetric(
							prometheus.NewDesc(s.metricName(name, vname), m.HelpText.Value, labels, nil),
							prometheus.UntypedValue,
							v,
							values...)
					}
				}
			}
		}(name, m)
	}
	wg.Wait()
}

func newServer(opts ...serverOption) *server {
	s := &server{
		metricRegex: regexp.MustCompile(metricNameRegex),
	}

	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *server) start(ctx context.Context) {
	sctx, cancel := context.WithCancel(ctx)
	s.srvCancelFn = cancel
START:
	select {
	case <-sctx.Done():
		log.Infof("server context Done: %v", sctx.Err())
		return
	default:
		s.config.baseConfig.OperState = operStarting
		s.updatePrometheusBaseTelemetry(sctx, s.config.baseConfig)

		registry := prometheus.NewRegistry()
		err := registry.Register(s)
		if err != nil {
			log.Errorf("failed to add exporter to prometheus registry: %v", err)
			time.Sleep(retryInterval)
			goto START
		}
		// create http server
		promHandler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError})

		mux := http.NewServeMux()
		if s.config.baseConfig.HttpPath.Value == "" {
			s.config.baseConfig.HttpPath.Value = "/"
		}
		mux.Handle(s.config.baseConfig.HttpPath.Value, promHandler)
		mux.Handle("/", new(healthHandler))

		var addr string
		if strings.Contains(s.config.baseConfig.Address.Value, ":") {
			addr = fmt.Sprintf("[%s]:%s", s.config.baseConfig.Address.Value, s.config.baseConfig.Port.Value)
		} else {
			addr = fmt.Sprintf("%s:%s", s.config.baseConfig.Address.Value, s.config.baseConfig.Port.Value)
		}
		s.srv = &http.Server{
			Addr:    addr,
			Handler: mux,
		}
		var netInstName string
		if netInst, ok := s.config.nwInst[s.config.baseConfig.NetworkInstance.Value]; ok {
			netInstName = fmt.Sprintf("%s-%s", netInst.BaseName, s.config.baseConfig.NetworkInstance.Value)
		} else {
			log.Errorf("unknown network instance name: %s", s.config.baseConfig.NetworkInstance.Value)
			return
		}
		log.Debugf("using network-instance %q", netInstName)
		n, err := netns.GetFromName(netInstName)
		if err != nil {
			log.Errorf("failed getting NS %q: %v", netInstName, err)
			time.Sleep(retryInterval)
			goto START
		}
		log.Debugf("got namespace: %+v", n)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		err = netns.Set(n)
		if err != nil {
			log.Infof("failed setting NS to %s: %v", n, err)
			time.Sleep(retryInterval)
			goto START
		}

		// create tcp listener
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			log.Infof("failed to create tcp listener: %v", err)
			time.Sleep(retryInterval)
			goto START
		}

		// start http server
		log.Infof("starting http server on %s", s.srv.Addr)
		s.config.baseConfig.OperState = operUp
		s.updatePrometheusBaseTelemetry(ctx, s.config.baseConfig)

		go func() {
			err = s.srv.Serve(listener)
			if err != nil && err != http.ErrServerClosed {
				log.Infof("prometheus server error: %v", err)
				s.config.baseConfig.OperState = operDown
				go s.updatePrometheusBaseTelemetry(sctx, s.config.baseConfig)
			}
			log.Print("http server closed...")
		}()
		go s.registerService(sctx)
	}
}

// assumes config is already locked
func (s *server) createSubscribeRequest(metricName string) (*gnmi.SubscribeRequest, error) {
	var paths []string
	if _, ok := s.config.metrics[metricName]; ok {
		paths = make([]string, 0, len(knownMetrics[metricName]))
		paths = append(paths, knownMetrics[metricName]...)
	} else if _, ok := s.config.customMetric[metricName]; ok {
		paths = make([]string, 0, len(s.config.customMetric[metricName].Metric.Paths))
		for _, value := range s.config.customMetric[metricName].Metric.Paths {
			paths = append(paths, value.Value)
		}
	} else {
		return nil, fmt.Errorf("unknown metric name %s", metricName)
	}
	numPaths := len(paths)
	if numPaths == 0 {
		return nil, fmt.Errorf("no paths found under metric %q", metricName)
	}
	subscriptions := make([]*gnmi.Subscription, numPaths)
	for i, p := range paths {
		gnmiPath, err := utils.ParsePath(p)
		if err != nil {
			return nil, fmt.Errorf("metric %q, path %q parse error: %v", metricName, p, err)
		}
		subscriptions[i] = &gnmi.Subscription{Path: gnmiPath}
	}
	return &gnmi.SubscribeRequest{
		Request: &gnmi.SubscribeRequest_Subscribe{
			Subscribe: &gnmi.SubscriptionList{
				Mode:         gnmi.SubscriptionList_ONCE,
				Encoding:     gnmi.Encoding_JSON_IETF,
				Subscription: subscriptions,
			},
		},
	}, nil
}

func (s *server) getLabels(ev *formatters.EventMsg) ([]string, []string) {
	labels := make([]string, 0, len(ev.Tags))
	values := make([]string, 0, len(ev.Tags))
	addedLabels := make(map[string]struct{})
	for k, v := range ev.Tags {
		labelName := s.metricRegex.ReplaceAllString(filepath.Base(k), "_")
		if _, ok := addedLabels[labelName]; ok {
			continue
		}
		labels = append(labels, labelName)
		values = append(values, v)
		addedLabels[labelName] = struct{}{}
	}
	return labels, values
}

func getFloat(v interface{}) (float64, error) {
	switch i := v.(type) {
	case float64:
		return float64(i), nil
	case float32:
		return float64(i), nil
	case int64:
		return float64(i), nil
	case int32:
		return float64(i), nil
	case int16:
		return float64(i), nil
	case int8:
		return float64(i), nil
	case uint64:
		return float64(i), nil
	case uint32:
		return float64(i), nil
	case uint16:
		return float64(i), nil
	case uint8:
		return float64(i), nil
	case int:
		return float64(i), nil
	case uint:
		return float64(i), nil
	case string:
		f, err := strconv.ParseFloat(i, 64)
		if err != nil {
			return math.NaN(), err
		}
		return f, err
	default:
		return math.NaN(), errors.New("getFloat: unknown value is of incompatible type")
	}
}

func (s *server) metricName(name, valueName string) string {
	// more customizations ?
	valueName = fmt.Sprintf("%s_%s", name, path.Base(valueName))
	return strings.TrimLeft(s.metricRegex.ReplaceAllString(valueName, "_"), "_")
}

func (s *server) shutdown(ctx context.Context, timeout time.Duration) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if s.srvCancelFn != nil {
		// stop any running registration goroutine
		s.srvCancelFn()
		s.config.baseConfig.Registration.OperState = operDown
	}
	if s.srv != nil {
		err := s.srv.Shutdown(cctx)
		if err != nil {
			log.Errorf("failed to shutdown prometheus server: %v", err)
		} else {
			log.Infof("prometheus server shutdown...")
		}
	}
	s.config.baseConfig.OperState = operDown
	s.updatePrometheusBaseTelemetry(ctx, s.config.baseConfig)
}

func (s *server) registerService(ctx context.Context) {
	if s.config.baseConfig.Registration.AdminState == adminDisable {
		return
	}
	if s.regCancelFn != nil {
		s.regCancelFn()
	}

	ctx, s.regCancelFn = context.WithCancel(ctx)
	defer s.regCancelFn()

	// set oper state to STARTING
	s.config.baseConfig.Registration.OperState = operStarting
	go s.updatePrometheusBaseTelemetry(ctx, s.config.baseConfig)

	log.Info("starting service registration...")

NETNS:
	if s.config.baseConfig.Registration.AdminState == adminDisable {
		return
	}
	// get network instance corresponding netns
	var netInstName string
	if netInst, ok := s.config.nwInst[s.config.baseConfig.NetworkInstance.Value]; ok {
		netInstName = fmt.Sprintf("%s-%s", netInst.BaseName, s.config.baseConfig.NetworkInstance.Value)
	} else {
		log.Errorf("unknown network instance name: %s", s.config.baseConfig.NetworkInstance.Value)
		return
	}
	log.Infof("using network-instance name %q", netInstName)

	n, err := netns.GetFromName(netInstName)
	if err != nil {
		log.Errorf("failed getting namespace for network-instance %q: %v", netInstName, err)
		time.Sleep(retryInterval)
		goto NETNS
	}
	defer n.Close()
	log.Infof("network instance %q netns: %s", netInstName, n.UniqueId())

INITCONSUL:
	if s.config.baseConfig.Registration.AdminState == adminDisable {
		return
	}
	trans := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			runtime.LockOSThread()
			err := netns.Set(n)
			if err != nil {
				return nil, fmt.Errorf("failed to set NetNS %q: %v", n.UniqueId(), err)
			}
			log.Infof("switched to netNS: %s", n.UniqueId())
			return (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 5 * time.Second,
				DualStack: true,
			}).DialContext(ctx, network, address)
		},
	}
	defer runtime.UnlockOSThread()

	clientConfig := &capi.Config{
		Address:   s.config.baseConfig.Registration.Address.Value,
		Scheme:    "http",
		Token:     s.config.baseConfig.Registration.Token.Value,
		Transport: trans,
	}
	if s.config.baseConfig.Registration.Username.Value != "" && s.config.baseConfig.Registration.Password.Value != "" {
		clientConfig.HttpAuth = &capi.HttpBasicAuth{
			Username: s.config.baseConfig.Registration.Username.Value,
			Password: s.config.baseConfig.Registration.Password.Value,
		}
	}
	s.consulClient, err = capi.NewClient(clientConfig)
	if err != nil {
		log.Errorf("failed to create Consul client: %v", err)
		time.Sleep(retryInterval)
		goto INITCONSUL
	}
	self, err := s.consulClient.Agent().Self()
	if err != nil {
		log.Errorf("failed to get Consul Agent details: %v", err)
		time.Sleep(retryInterval)
		goto INITCONSUL
	}
	if cfg, ok := self["Config"]; ok {
		b, _ := json.Marshal(cfg)
		log.Infof("consul agent config: %s", string(b))
	}

	systemInfo, err := s.getSystemInfo(ctx)
	if err != nil {
		log.Errorf("failed to connect to consul: %v", err)
		time.Sleep(retryInterval)
		goto INITCONSUL
	}
	addr := systemInfo.IPAddrV4
	if addr == "" {
		addr = systemInfo.IPAddrV6
	}

	port, _ := strconv.Atoi(s.config.baseConfig.Port.Value)

	tags := make([]string, 0, len(s.config.baseConfig.Registration.Tags)+6)
	for _, t := range s.config.baseConfig.Registration.Tags {
		tags = append(tags, t.Value)
	}
	tags = append(tags,
		fmt.Sprintf("version=%s", systemInfo.Version),
		fmt.Sprintf("chassis-type=%s", systemInfo.ChassisType),
		fmt.Sprintf("chassis-mac-address=%s", systemInfo.ChassisMacAddress),
		fmt.Sprintf("chassis-part-number=%s", systemInfo.ChassisPartNumber),
		fmt.Sprintf("chassis-serial-number=%s", systemInfo.ChassisSerialNumber),
		fmt.Sprintf("chassis-clei-code=%s", systemInfo.ChassisCLEICode),
	)

	service := &capi.AgentServiceRegistration{
		ID:      systemInfo.Name,
		Name:    serviceName,
		Address: addr,
		Port:    port,
		Tags:    tags,
		Checks: capi.AgentServiceChecks{
			{
				TTL:                            s.config.baseConfig.Registration.TTL.Value,
				DeregisterCriticalServiceAfter: s.config.baseConfig.Registration.TTL.Value,
			},
		},
	}

	ttlCheckID := "service:" + systemInfo.Name
	if s.config.baseConfig.Registration.HTTPCheck.Value {
		service.Checks = append(service.Checks, &capi.AgentServiceCheck{
			HTTP:                           fmt.Sprintf("http://%s:%s", addr, s.config.baseConfig.Port.Value),
			Method:                         "GET",
			Interval:                       s.config.baseConfig.Registration.TTL.Value,
			TLSSkipVerify:                  true,
			DeregisterCriticalServiceAfter: s.config.baseConfig.Registration.TTL.Value,
		})
		ttlCheckID = ttlCheckID + ":1"
	}
	b, _ := json.Marshal(service)
	log.Infof("registering service: %s", string(b))
	err = s.consulClient.Agent().ServiceRegister(service)
	if err != nil {
		log.Errorf("failed to register service in consul: %v", err)
		time.Sleep(retryInterval)
		goto INITCONSUL
	}
	s.config.baseConfig.Registration.OperState = operUp
	go s.updatePrometheusBaseTelemetry(ctx, s.config.baseConfig)
	err = s.consulClient.Agent().UpdateTTL(ttlCheckID, "", capi.HealthPassing)
	if err != nil {
		log.Errorf("failed to pass the first TTL check: %v", err)
		time.Sleep(retryInterval)
		goto INITCONSUL
	}
	ttl, _ := time.ParseDuration(s.config.baseConfig.Registration.TTL.Value)
	ticker := time.NewTicker(ttl / 2)

	for {
		select {
		case <-ticker.C:
			// check if the registration was disabled since last update
			if s.config.baseConfig.Registration.AdminState == adminDisable {
				s.consulClient.Agent().ServiceDeregister(systemInfo.Name)
				s.config.baseConfig.Registration.OperState = operDown
				go s.updatePrometheusBaseTelemetry(ctx, s.config.baseConfig)
				ticker.Stop()
				return
			}
			// check if systemName was changed since last update
			newSysInfo, err := s.getSystemInfo(ctx)
			if err != nil {
				// failed to get systemName: deregister, recreate Consul client and re register
				s.consulClient.Agent().ServiceDeregister(systemInfo.Name)
				ticker.Stop()
				goto INITCONSUL
			}
			// systemName changed: deregister, recreate Consul client and re register
			if systemInfo.Name != newSysInfo.Name {
				ticker.Stop()
				s.consulClient.Agent().ServiceDeregister(systemInfo.Name)
				goto INITCONSUL
			}
			// no change in systemName: update Service TTL
			err = s.consulClient.Agent().UpdateTTL(ttlCheckID, "", capi.HealthPassing)
			if err != nil {
				log.Errorf("failed to pass TTL check: %v", err)
			}
		case <-ctx.Done():
			s.consulClient.Agent().ServiceDeregister(systemInfo.Name)
			ticker.Stop()
			return
		}
	}
}

func (s *server) getSystemInfo(ctx context.Context) (*systemInfo, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "username", s.config.username, "password", s.config.password)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
START:
	select {
	case <-sctx.Done():
		return nil, ctx.Err()
	default:
		conn, gnmiClient, err := createGNMIClient(sctx)
		if err != nil {
			log.Infof("failed to create a gnmi connection to %q: %v", gnmiServerUnixSocket, err)
			time.Sleep(retryInterval)
			goto START
		}
		defer conn.Close()

		rsp, err := gnmiClient.Get(sctx,
			&gnmi.GetRequest{
				Path:     sysInfoPaths,
				Type:     gnmi.GetRequest_STATE,
				Encoding: gnmi.Encoding_ASCII,
			})
		if err != nil {
			log.Errorf("failed Get response: %v", err)
			time.Sleep(retryInterval)
			goto START
		}
		sysInfo := new(systemInfo)

		for _, n := range rsp.GetNotification() {
			for _, u := range n.GetUpdate() {
				path := utils.GnmiPathToXPath(u.GetPath(), true)
				if strings.HasPrefix(path, "interface") {
					if strings.Contains(path, "/ipv4/address/status") {
						ip := getPathKeyVal(u.GetPath(), "address", "ip-prefix")
						sysInfo.IPAddrV4 = strings.Split(ip, "/")[0]
					}
					if strings.Contains(path, "/ipv6/address/status") {
						ip := getPathKeyVal(u.GetPath(), "address", "ip-prefix")
						sysInfo.IPAddrV6 = strings.Split(ip, "/")[0]
					}
				}
				if strings.Contains(path, "system/name") {
					sysInfo.Name = u.GetVal().GetStringVal()
				}
				if strings.Contains(path, "system/information/version") {
					sysInfo.Version = u.GetVal().GetStringVal()
				}
				if strings.Contains(path, "platform/chassis/type") {
					sysInfo.ChassisType = u.GetVal().GetStringVal()
				}
				if strings.Contains(path, "platform/chassis/mac-address") {
					sysInfo.ChassisMacAddress = u.GetVal().GetStringVal()
				}
				if strings.Contains(path, "platform/chassis/part-number") {
					sysInfo.ChassisPartNumber = u.GetVal().GetStringVal()
				}
				if strings.Contains(path, "platform/chassis/clei-code") {
					sysInfo.ChassisCLEICode = u.GetVal().GetStringVal()
				}
				if strings.Contains(path, "platform/chassis/serial-number") {
					sysInfo.ChassisSerialNumber = u.GetVal().GetStringVal()
				}
			}
		}
		log.Debugf("system info: %+v", sysInfo)
		return sysInfo, nil
	}
}

func getPathKeyVal(p *gnmi.Path, elem, key string) string {
	for _, e := range p.GetElem() {
		if e.Name == elem {
			return e.Key[key]
		}
	}
	return ""
}

type healthHandler struct{}

func (h *healthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "OK")
}

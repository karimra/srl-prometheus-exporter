package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/metadata"

	"github.com/google/gnxi/utils/xpath"
	"github.com/karimra/gnmic/collector"
	"github.com/karimra/srl-ndk-demo/agent"
	"github.com/openconfig/gnmi/proto/gnmi"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/vishvananda/netns"
)

const (
	metricNameRegex = "[^a-zA-Z0-9_]+"
)

type server struct {
	config *config
	agent  *agent.Agent

	srv         *http.Server
	srvCancelFn context.CancelFunc
	//isStarting  bool
	metricRegex *regexp.Regexp
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

	gnmiClient, err := createGNMIClient(gctx)
	if err != nil {
		log.Infof("failed to create a gnmi connection to '%s': %v", gnmiServerUnixSocket, err)
		return
	}

	// lock config
	s.config.m.Lock()
	defer s.config.m.Unlock()

	// get metrics that are enabled
	metrics := make(map[string]*metricConfig)
	for name, m := range s.config.metrics {
		if m.Metric.State == stateEnable {
			metrics[name] = m
		}
	}
	for name, m := range s.config.customMetric {
		if m.Metric.State == stateEnable {
			metrics[name] = m
		}
	}
	log.Debugf("about to collect metrics: %v", metrics)
	ctx, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	wg := new(sync.WaitGroup)
	wg.Add(len(metrics))
	for name, m := range metrics {
		go func(name string, m *metricConfig) {
			defer wg.Done()
			log.Debugf("collecting metric '%s'", name)
			req, err := s.createSubscribeRequest(name)
			if err != nil {
				return
			}
			sctx, cancel := context.WithCancel(ctx)
			defer cancel()
			subClient, err := gnmiClient.Subscribe(sctx)
			if err != nil {
				log.Errorf("failed to create a subscribe client for metric '%s': %v", name, err)
				return
			}
			log.Debugf("sending subscribe request: %+v", req)
			err = subClient.Send(req)
			if err != nil {
				log.Errorf("failed to send a subscribe request for metric '%s': %v", name, err)
				return
			}
			for {
				subResp, err := subClient.Recv()
				if err == io.EOF {
					log.Infof("subscription for metric '%s' received EOF, subscription done", name)
					return
				}
				if err != nil {
					log.Errorf("failed to receive a subscribe request for metric '%s': %v", name, err)
					return
				}
				select {
				case <-sctx.Done():
					return
				default:
					log.Debugf("received subscribe response: %+v", subResp)
					events, err := collector.ResponseToEventMsgs("", subResp, nil)
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
							log.Printf("metric: %s : %+v", name, m.Metric)
							ch <- prometheus.MustNewConstMetric(
								prometheus.NewDesc(s.metricName(vname), m.Metric.HelpText.Value, labels, nil),
								prometheus.UntypedValue,
								v,
								values...)
						}
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
		return
	default:
		registry := prometheus.NewRegistry()
		err := registry.Register(s)
		if err != nil {
			log.Errorf("failed to add exporter to prometheus registery: %v", err)
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
		log.Debugf("using network-instance '%s'", netInstName)
		n, err := netns.GetFromName(netInstName)
		if err != nil {
			log.Errorf("failed getting NS '%s': %v", netInstName, err)
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
		err = s.srv.Serve(listener)
		if err != nil && err != http.ErrServerClosed {
			log.Infof("prometheus server error: %v", err)
			s.config.baseConfig.OperState = operDown
			go s.updatePrometheusBaseTelemetry(ctx, s.config.baseConfig)
		}
		log.Print("http server closed...")
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
		return nil, fmt.Errorf("no paths found under metric '%s'", metricName)
	}
	subscriptions := make([]*gnmi.Subscription, numPaths)
	for i, p := range paths {
		gnmiPath, err := xpath.ToGNMIPath(p)
		if err != nil {
			return nil, fmt.Errorf("metric '%s', path '%s' parse error: %v", metricName, p, err)
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

func (s *server) getLabels(ev *collector.EventMsg) ([]string, []string) {
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

func (s *server) metricName(valueName string) string {
	// more customisations ?
	return strings.TrimLeft(s.metricRegex.ReplaceAllString(valueName, "_"), "_")
}

func (s *server) shutdown(ctx context.Context, timeout time.Duration) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	s.srvCancelFn()
	if s.srv != nil {
		err := s.srv.Shutdown(cctx)
		if err != nil {
			log.Errorf("failed to shutdwon prometheus server: %v", err)
		} else {
			log.Infof("prometheus server shutdown...")
		}
	}
	s.config.baseConfig.OperState = operDown
	s.updatePrometheusBaseTelemetry(ctx, s.config.baseConfig)
}

package main

import (
	"context"
	"encoding/json"
	"fmt"

	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/prototext"

	ndk "github.com/karimra/go-srl-ndk"
)

func (s *server) updateTelemetry(ctx context.Context, jsPath string, jsData string) {
	key := &ndk.TelemetryKey{JsPath: jsPath}
	data := &ndk.TelemetryData{JsonContent: jsData}
	info := &ndk.TelemetryInfo{Key: key, Data: data}
	telReq := &ndk.TelemetryUpdateRequest{
		State: []*ndk.TelemetryInfo{info},
	}
	log.Debugf("Updating telemetry with: %+v", telReq)
	b, err := prototext.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(telReq)
	if err != nil {
		log.Errorf("telemetry request Marshal failed: %+v", err)
	}
	fmt.Printf("%s\n", string(b))
	r1, err := s.agent.TelemetryService.Client.TelemetryAddOrUpdate(ctx, telReq)
	if err != nil {
		log.Errorf("Could not update telemetry key=%s: err=%v", jsPath, err)
		return
	}
	log.Debugf("Telemetry add/update status: %s, error_string: '%s'", r1.GetStatus().String(), r1.GetErrorStr())
}

func (s *server) deleteTelemetry(ctx context.Context, jsPath string) error {
	key := &ndk.TelemetryKey{JsPath: jsPath}
	telReq := &ndk.TelemetryDeleteRequest{}
	telReq.Key = make([]*ndk.TelemetryKey, 0)
	telReq.Key = append(telReq.Key, key)

	b, err := prototext.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(telReq)
	if err != nil {
		log.Errorf("telemetry request Marshal failed: %+v", err)
	}
	fmt.Printf("%s\n", string(b))

	r1, err := s.agent.TelemetryService.Client.TelemetryDelete(ctx, telReq)
	if err != nil {
		log.Errorf("could not delete telemetry for key : %s", jsPath)
		return err
	}
	log.Debugf("telemetry delete status: %s, error_string: '%s'", r1.GetStatus().String(), r1.GetErrorStr())
	return nil
}

func (s *server) updatePrometheusBaseTelemetry(ctx context.Context, cfg *baseConfig) {
	jsData, err := json.Marshal(cfg)
	if err != nil {
		log.Errorf("failed to marshal json data: %v", err)
		return
	}
	s.updateTelemetry(ctx, exporterPath, string(jsData))
}

// metrics
func (s *server) updateMetricTelemetry(ctx context.Context, name string, cfg *metricConfig) {
	jsData, err := json.Marshal(cfg)
	if err != nil {
		log.Errorf("failed to marshal json data: %v", err)
		return
	}
	s.updateTelemetry(ctx, fmt.Sprintf("%s{.name==\"%s\"}", metricPath, name), string(jsData))
}

func (s *server) deleteMetricTelemetry(ctx context.Context, name string) {
	jsPath := fmt.Sprintf("%s{.name==\"%s\"}", metricPath, name)
	log.Infof("Deleting telemetry path %s", jsPath)
	s.deleteTelemetry(ctx, jsPath)
}

// custom metrics
func (s *server) updateCustomMetricTelemetry(ctx context.Context, name string, cfg *metricConfig) {
	jsData, err := json.Marshal(cfg)
	if err != nil {
		log.Errorf("failed to marshal json data: %v", err)
		return
	}
	s.updateTelemetry(ctx, fmt.Sprintf("%s{.name==\"%s\"}", customMetricPath, name), string(jsData))
}

func (s *server) deleteCustomMetricTelemetry(ctx context.Context, name string) {
	jsPath := fmt.Sprintf("%s{.name==\"%s\"}", customMetricPath, name)
	log.Infof("Deleting telemetry path %s", jsPath)
	s.deleteTelemetry(ctx, jsPath)
}

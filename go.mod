module github.com/karimra/srl-prometheus-exporter

go 1.16

require (
	github.com/google/gnxi v0.0.0-20201015131541-8b27e9559e9b
	github.com/hashicorp/consul/api v1.3.0
	github.com/karimra/gnmic v0.19.0
	github.com/karimra/go-srl-ndk v0.0.0-20200804123439-2dac490c3a6a
	github.com/karimra/srl-ndk-demo v0.0.0-20200814075950-49a03fa7ce13
	github.com/openconfig/gnmi v0.0.0-20210707145734-c69a5df04b53
	github.com/prometheus/client_golang v1.8.0
	github.com/sirupsen/logrus v1.7.0
	github.com/vishvananda/netns v0.0.0-20200728191858-db3c7e526aae
	golang.org/x/net v0.0.0-20210805182204-aaa1db679c0d // indirect
	golang.org/x/sys v0.0.0-20210921065528-437939a70204 // indirect
	google.golang.org/grpc v1.48.0
	google.golang.org/protobuf v1.28.1
	gopkg.in/yaml.v2 v2.4.0
)

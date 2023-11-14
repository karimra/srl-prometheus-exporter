#!/bin/bash

# credentials
username=admin
password=NokiaSrl1!

# build comma separated srl nodes names
nodes=$(docker ps -f label=clab-node-kind=srl -f label=containerlab=prom-exporter --format {{.Names}} | paste -s -d, -)

gnmic_args="-u $username -p $password -a $nodes --skip-verify"

# script path
SCRIPTPATH="$( cd -- "$(dirname "$0")" >/dev/null 2>&1 ; pwd -P )"

# configure app prerequisites, gNMI UDS and ACLs
gnmic $gnmic_args set --request-file config/app/config.yaml

# load basic nodes config, p2p links
gnmic $gnmic_args set --request-file config/interfaces/template.gotmpl --request-vars config/interfaces/vars.yaml

# check the app config, it should be admin down and oper down
gnmic $gnmic_args -e json_ietf \
                get \
                --path /system/prometheus-exporter

# enable metrics "interfaces" and "subinterfaces"
gnmic $gnmic_args -e json_ietf \
                set \
                --update-path /system/prometheus-exporter/metric[name=interfaces]/state \
                --update-value enable \
                --update-path /system/prometheus-exporter/metric[name=subinterfaces]/state \
                --update-value enable

# enable consul registration
## get consul agent IP
consul_ip=$(docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' clab-prom-exporter-consul-agent)
echo "consul IP address:" ${consul_ip}

gnmic $gnmic_args -e json_ietf \
                set \
                --update-path /system/prometheus-exporter/registration/address \
                --update-value ${consul_ip}:8500 \
                --update-path /system/prometheus-exporter/registration/admin-state \
                --update-value enable

# enable the prometheus exporter app
gnmic $gnmic_args -e json_ietf \
                set \
                --update-path /system/prometheus-exporter/admin-state \
                --update-value enable

# check that the SRLs prometheus endpoint is UP
curl -sSL clab-prom-exporter-srl1:8888/metrics
curl -sSL clab-prom-exporter-srl2:8888/metrics

# navigate to the prometheus server GUI on <your serverIP>:9090/targets
# you should see that both SRL prometheus exporters are UP and being scraped by Prometheus
#!/bin/bash

./stop.sh

version=0.2.8
username=admin
password=NokiaSrl1!

filename="srl-prometheus-exporter_${version}_Linux_x86_64.rpm"

rm -rf app
mkdir -p app

if [[ $1 == "build" ]]; then
    # build RPM
    echo "building using goreleaser"
    cd ../..
    goreleaser --snapshot --clean release
    cp dist/*.rpm labs/clab/app/
    cd labs/clab
else
    # download the app installation RPM
    echo "downloading the app RPM"
    curl -sSL https://github.com/karimra/srl-prometheus-exporter/releases/download/v${version}/${filename} -o app/${filename}
fi

#deploy the lab
sudo clab dep -c

# build comma separated srl nodes names
nodes=$(docker ps -f label=clab-node-kind=srl -f label=containerlab=prom-exporter --format {{.Names}} | paste -s -d, -)

gnmic_args="-u $username -p $password -a $nodes --skip-verify"
# configure app prerequisites, gNMI UDS and ACLs
gnmic $gnmic_args set --request-file config/app/config.yaml

# load basic nodes config, p2p links
gnmic $gnmic_args set --request-file config/interfaces/template.gotmpl --request-vars config/interfaces/vars.yaml

clab_exec_args="--topo prometheus-exporter.clab.yaml --label clab-node-kind=srl --label containerlab=prom-exporter --cmd"

# check the applications installed in the SRLs
sudo clab exec $clab_exec_args "sr_cli show system application"

# install the RPM located in /tmp/rpm
sudo clab exec $clab_exec_args "sudo rpm -U /tmp/rpm/*rpm"

# reload the app manager so it picks up the newly installed app
sudo clab exec $clab_exec_args "sr_cli tools system app-management application app_mgr reload"

# check the app status in both SRLs
sudo clab exec $clab_exec_args "sr_cli show system application prometheus-exporter | as json"

sleep 10
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
# curl clab-prom-exporter-srl1:8888/metrics
# curl clab-prom-exporter-srl2:8888/metrics

# navigate to the prometheus server GUI on <your serverIP>:9090/targets
# you should see that both SRL prometheus exporters are UP and being scraped by Prometheus
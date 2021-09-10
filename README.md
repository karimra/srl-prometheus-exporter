# srl-prometheus-exporter

This repo hosts a SR Linux NDK agent that exposes a Prometheus endpoint ready to be scraped by a Prometheus Server.

It uses SRL's gnmi-server over the unix socket address to retrieve gNMI updates, it then builds the Prometheus metrics and exposes them through a configurable HTTP address.

### Prerequisites:

- gNMI server unix socket is enabled
- The configured HTTP address and port are accepted by SRL CPM filters

### Installation

Download the RPM from the github releases page, copy it to your SRL instance and run:
```bash
sudo rpm -i srl-prometheus-exporter_0.1.0_Linux_x86_64.rpm
```

Connect to your SRL instance and reload the application manager
```bash
--{ + running }--[  ]--                                                                                                                                              
A:srl1# tools system app-management application app_mgr reload
```

Check that the `prometheus-exporter` app is now running
```bash
A:srl1# show system application | grep prometheus-exporter                                                                                                           
  | prometheus-exporter | 2208 | running            |                                    | 2021-09-10T21:31:30.691Z |
--{ + running }--[  ]--     
```

### Configuration:

The metrics exposed can be configured using SRL's CLI

```text
--{ + running }--[ system prometheus-exporter ]--
A:srl1# info detail
    address ::
    port 8888
    network-instance mgmt
    http-path /metrics
    admin-state enable
    metric interfaces {
        state enable
        help-text "SRLinux generated metric"
    }
```

The predefined metric names and the corresponding gNMI paths used to build the metrics are as below: 

|               metric name              	|                            gNMI paths                          	|
|:--------------------------------------:	|:--------------------------------------------------------------:	|
|               "interfaces"             	|     "/interface/statistics"                                    	|
|                                        	|     "/interface/ethernet/statistics"                          	|
|                                        	|     "/interface/queue-statistics"                              	|
|                                        	|     "/interface/lag/members/statistics"                       	|
|             "subinterfaces"            	|     "/interface/subinterface/statistics"                       	|
|                  "lldp"                	|     "/system/lldp/interface/statistics"                        	|
|                "platform"              	|     "/platform/control/disk/statistics"                        	|
|                                        	|     "/platform/control/cpu/software-interrupt"                 	|
|                                        	|     "/platform/control/memory"                                 	|
|                                        	|     "/platform/linecard/forwarding-complex/buffer-memory"      	|
|                  "acl"                 	|     "/acl/policers/system-cpu-policer/statistics"              	|
|                                        	|     "/acl/policers/policer/statistics"                         	|
|                                        	|     "/acl/ipv4-filter/entry/statistics"                        	|
|                                        	|     "/acl/ipv6-filter/entry/statistics"                        	|
|                                        	|     "/acl/cpm-filter/ipv4-filter/entry/statistics"             	|
|                                        	|     "/acl/cpm-filter/ipv6-filter/entry/statistics"             	|
|                  "aaa"                 	|     "/system/aaa/server-group/server/statistics"               	|
|     "network-instance-bridge-table"    	|     "/network-instance/bridge-table/statistics"                	|
|         "network-instance-icmp"        	|     "/network-instance/icmp/statistics"                        	|
|         "network-instance-icmp6"       	|     "/network-instance/icmp6/statistics"                       	|
|        "route-table-ipv4-unicast"      	|     "/network-instance/route-table/ipv4-unicast/statistics"    	|
|        "route-table-ipv6-unicast"      	|     "/network-instance/route-table/ipv6-unicast/statistics"    	|
|                  "mpls"                	|     "/network-instance/route-table/mpls/statistics"            	|
|                  "isis"                	|     "/network-instance/protocols/isis/instance/statistics"     	|
|                  "bgp"                 	|     "/network-instance/protocols/bgp/group/statistics"         	|
|                  "udp"                 	|     "/network-instance/udp/statistics"                         	|
|                  "tcp"                 	|     "/network-instance/tcp/statistics"                         	|

The predefined paths and metrics names can be changed at startup using a configuration file.

Custom metrics can be added at runtime using SRL's CLI/gNMI/JSON-RPC interfaces as below:

```text
--{ + candidate shared default }--[ system prometheus-exporter ]--
A:srl1# custom-metric my_metric paths [ /network-instance/protocols/bgp/statistics ]
--{ +* candidate shared default }--[ system prometheus-exporter ]-- 
A:srl1# commit now
All changes have been committed. Leaving candidate mode.
--{ + running }--[ system prometheus-exporter ]--
A:srl1# info                                                                                                                                                      
    // snipped...
    custom-metric my_metric {
        paths [
            /network-instance/protocols/bgp/statistics
        ]
    }
```

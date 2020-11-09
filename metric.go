package main

var knownMetrics = map[string][]string{
	"interfaces": {
		"/interface/statistics",
		"/interface/ethernet/statistics/",
		"/interface/queue-statistics",
		"/interface/lag/members/statistics/",
	},
	"subinterfaces": {
		"/interface/subinterface/statistics",
	},
	"lldp": {
		"/system/lldp/interface/statistics",
	}, // interface is enough, no need for /system/lldp/statistics
	"platform": {
		"/platform/control/disk/statistics",
		"/platform/control/cpu/software-interrupt",
		"/platform/control/memory",
		"/platform/linecard/forwarding-complex/buffer-memory",
	},
	"acl": {
		"/acl/policers/system-cpu-policer[name=*]/statistics",
		"/acl/policers/policer[name=*]/statistics",
		"/acl/ipv4-filter[name=*]/entry[sequence-id=*]/statistics",
		"/acl/ipv6-filter[name=*]/entry[sequence-id=*]/statistics",
		"/acl/cpm-filter/ipv4-filter/entry[sequence-id=*]/statistics",
		"/acl/cpm-filter/ipv6-filter/entry[sequence-id=*]/statistics",
	},
	"aaa": {
		"/system/aaa/server-group/server/statistics",
	},
	"network-instance-bridge-table": {
		"/network-instance/bridge-table/statistics",
	},
	"network-instance-icmp": {
		"/network-instance/icmp/statistics",
	},
	"network-instance-icmp6": {
		"/network-instance/icmp6/statistics",
	},
	"route-table-ipv4-unicast": {
		"/network-instance/route-table/ipv4-unicast/statistics",
	},
	"route-table-ipv6-unicast": {
		"/network-instance/route-table/ipv6-unicast/statistics",
	},
	"mpls": {
		"/network-instance/route-table/mpls/statistics",
	},
	"isis": {
		"/network-instance/protocols/isis/instance/statistics",
	},
	"bgp": {
		"/network-instance/protocols/bgp/group/statistics",
	},
	"udp": {
		"/network-instance/udp/statistics",
	},
	"tcp": {
		"/network-instance/tcp/statistics",
	},
}

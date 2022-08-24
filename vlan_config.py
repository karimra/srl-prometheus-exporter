# 0..1499
for vlan in range(1500-5):
  v = vlan+6
  print( f"/interface ethernet-1/1 subinterface {v} vlan encap single-tagged vlan-id {v}" )
  print( f"/interface ethernet-1/1 subinterface {v} ipv4 address 10.{int(v / 256)}.{int(v % 256)}.1/24" )
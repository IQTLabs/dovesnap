# v0.9.0 (2020-10-21)

- Move non-error OVS debug to trace level
- Add support for setting container/NAT interface MAC prefixes.

# v0.8.0 (2020-10-09)

- add JSON endpoint to get dovesnap state
- log MAC address of containers
- Update faucet, faucetconfrpc, certstrap, grafana, grpc

# v0.7.0 (2020-09-30)

- allow ACL on NAT port
- tests check mirror flows are present
- mirror bridge should pass through packets with any VLAN tag
- fix viz crash when no ip command
- upgrade logrus, faucetconfrpc, grafana

# v0.6.0 (2020-09-22)

- fix race condition that could lead to multiple containers with same OFPort
- add NAT port mapping (-p) support via network gateway IP
- Remove OVS privilege when in userspace mode
- Move to go mod from dep ensure
- Viz script timeout/missing IP fixes
- update faucetconfrpc, OVS

# v0.5.0 (2020-09-04)

- update faucetconfrpc, certstrap
- graph_dovesnap vizualization script
- support OVS userspace mode

# v0.4.0 (2020-08-27)

- add/remove non-dovesnap managed ports to bridges to FAUCET
- ARM compatibility
- Updated grafana, faucetconfrpc

# v0.3.0 (2020-08-21)

- Add option for using DHCP
- Ability to restart dovesnap and have it recovering existing networks
- Updated grafana, faucet and gauge

# v0.2.0 (2020-08-14)

- Implement mirroring, stacking, FAUCET configuration via RPC.

# v0.1.1 (2020-05-21)

- Initial release (fork from https://github.com/gopher-net/docker-ovs-plugin).
- Can create both NAT and flat networks and can add existing ports to a bridge/network
- Does not yet support fixed OFPort allocations for containers.

dovesnap
=================

### QuickStart Instructions

The quickstart instructions describe how to start the plugin. There are two modes, **nat** and **flat** described in the following section. At the moment, flat mode is the default.

**1.** Make sure you are using Docker 1.9 or later

**2.** You need to `modprobe openvswitch` on the machine where the Docker Daemon is located

```
$ sudo modprobe openvswitch
```

**3.** `docker-compose up -d --build`

**4.** Now you are ready to create a new network

```
$ docker network create -d ovs mynet
```

**5.** Test it out!

```
$ docker run -itd --net=mynet --name=web nginx

$ docker run -it --rm --net=mynet busybox wget -qO- http://web
```

### Modes

There are two generic modes, `flat` and `nat`. The default mode is `flat`.

`nat` does not require any orchestration with the network because the address space is hidden behind iptables masquerading.

- flat is simply an OVS bridge with the container link attached to it. An example would be a Docker host is plugged into a data center port that has a subnet of `192.168.1.0/24`. You would start the plugin like so:

```
$ docker network create --gateway=192.168.1.1 --subnet=192.168.1.0/24 -d ovs mynet
```

- Containers now start attached to an OVS bridge. It could be tagged or untagged but either way it is isolated and unable to communicate to anything outside of its bridge domain. In this case, you either add VXLAN tunnels to other bridges of the same bridge domain or add an `eth` interface to the bridge to allow access to the underlying network when traffic leaves the Docker host. To do so, you simply add the `eth` interface to the ovs bridge. Neither the bridge nor the eth interface need to have an IP address since traffic from the container is strictly L2. **Warning** if you are remoted into the physical host make sure you are not using an ethernet interface to attach to the bridge that is also your management interface since the eth interface no longer uses the IP address it had. The IP would need to be migrated to ovsbr-docker0 in this case. Allowing underlying network access to an OVS bridge can be done like so:

```
ovs-vsctl add-port ovsbr-docker0 eth2

```

Add an address to ovsbr-docker0 if you want an L3 interface on the L2 domain for the Docker host if you would like one for troubleshooting etc but it isn't required since flat mode cares only about MAC addresses and VLAN IDs like any other L2 domain would.

- Example of OVS with an ethernet interface bound to it for external access to the container sitting on the same bridge. NAT mode doesn't need the eth interface because IPTables is doing NAT/PAAT instead of bridging all the way through.


```
$ ovs-vsctl show
e0de2079-66f0-4279-a1c8-46ba0672426e
    Manager "ptcp:6640"
        is_connected: true
    Bridge "ovsbr-docker0"
        Port "ovsbr-docker0"
            Interface "ovsbr-docker0"
                type: internal
        Port "ovs-veth0-d33a9"
            Interface "ovs-veth0-d33a9"
        Port "eth2"
            Interface "eth2"
    ovs_version: "2.3.1"
```

**Flat Mode Note:** Hosts will only be able to ping one another unless you add an ethernet interface to the `docker-ovsbr0` bridge with something like `ovs-vsctl add-port <bridge_name> <port_name>`. NAT mode will masquerade around that issue. It is an inherent hastle of bridges that is unavoidable. This is a reason bridgeless implementation [gopher-net/ipvlan-docker-plugin](https://github.com/gopher-net/ipvlan-docker-plugin) and [gopher-net/macvlan-docker-plugin](https://github.com/gopher-net/macvlan-docker-plugin) can be attractive.

### Thanks

Thanks to the folks who wrote the orginal [docker-ovs-plugin](https://github.com/gopher-net/docker-ovs-plugin) which is what this project was forked from.

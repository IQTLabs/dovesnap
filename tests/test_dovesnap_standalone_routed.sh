#!/bin/bash

source ./tests/lib_test.sh

init_dirs
conf_faucet
conf_gauge
conf_keys
init_ovs

sudo ip link add odsaddport1 type veth peer name odsaddport2 && true

echo starting dovesnap infrastructure
docker-compose build && FAUCET_PREFIX=$TMPDIR docker-compose -f docker-compose.yml -f docker-compose-standalone.yml up -d || exit 1
wait_faucet

docker ps -a
echo creating testnet
docker network create testnet -d dovesnap --internal -o ovs.bridge.mode=routed -o ovs.bridge.dpid=0x1 -o ovs.bridge.controller=tcp:127.0.0.1:6653,tcp:127.0.0.1:6654 -o ovs.bridge.ovs_local_mac=0e:01:00:00:00:23 -o ovs.bridge.vlan_out_acl=allowall -o ovs.bridge.add_ports=odsaddport1/888/denyall -o ovs.bridge.mtu=1400 -o ovs.bridge.default_acl=denyall -o ovs.bridge.preallocate_ports=10 || exit 1
docker network ls
restart_dovesnap
echo creating testcon
# github test runner can't use ping.
docker pull busybox
docker run -d --label="dovesnap.faucet.portacl=testnet:ratelimitit" --label="dovesnap.faucet.mac_prefix=0e:99" --net=testnet --rm --name=testcon -p 80:80 busybox sleep 1h
RET=$?
if [ "$RET" != "0" ] ; then
	echo testcon container creation returned: $RET
	exit 1
fi
wait_acl
sudo grep -q "description: /testcon" $FAUCET_CONFIG || exit 1
echo verifying networking
GW=$(docker inspect testnet|jq -r '.[0]["IPAM"]["Config"][0]["Gateway"]')
docker exec -t testcon ping -c 3 $GW || exit 1
docker exec -t testcon ifconfig eth0 |grep -iq 0e:99 || exit 1
ip link | grep -iq 0e:01:00:00:00:23 || exit 1

clean_dirs

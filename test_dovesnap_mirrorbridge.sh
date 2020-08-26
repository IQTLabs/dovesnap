#!/bin/bash

source ./lib_test.sh

sudo ip link add mirrori type veth peer name mirroro
sudo ip link set dev mirrori up || exit 1
sudo ip link set dev mirroro up || exit 1

init_dirs
conf_faucet
conf_gauge
conf_keys

echo starting dovesnap infrastructure
docker-compose build || exit 1
init_ovs

FAUCET_PREFIX=$TMPDIR MIRROR_BRIDGE_OUT=mirrori docker-compose -f docker-compose.yml -f docker-compose-standalone.yml up -d || exit 1
wait_faucet

docker ps -a
echo creating testnet
docker network create testnet -d ovs --internal -o ovs.bridge.mode=nat -o ovs.bridge.dpid=0x1 -o ovs.bridge.controller=tcp:127.0.0.1:6653,tcp:127.0.0.1:6654 || exit 1
docker network ls
restart_wait_dovesnap
echo creating testcon
# github test runner can't use ping.
docker pull busybox
docker run -d --label="dovesnap.faucet.portacl=allowall" --label="dovesnap.faucet.mirror=true" --net=testnet --rm --name=testcon busybox sleep 1h
RET=$?
if [ "$RET" != "0" ] ; then
	echo testcon container creation returned: $RET
	exit 1
fi
wait_acl
wait_mirror
sudo grep -q "description: /testcon" $FAUCET_CONFIG || exit 1
echo verifying networking
sudo timeout 30s tcpdump -n -c 1 -U -i mirroro -w $MIRROR_PCAP tcp &
sleep 3
docker exec -t testcon wget -q -O- bing.com || exit 1
PCAPMATCH=TCP
wait_for_pcap_match
clean_dirs

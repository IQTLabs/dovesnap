#!/bin/bash

source ./lib_test.sh

init_dirs
conf_gauge
conf_keys

sudo rm -f $FAUCET_CONFIG
cat >$FAUCET_CONFIG <<EOFC || exit 1
meters:
  lossymeter:
    meter_id: 1
    entry:
        flags: "KBPS"
        bands: [{type: "DROP", rate: 100}]
acls:
  ratelimitit:
  - rule:
      actions:
        meter: lossymeter
        allow: 1
  allowall:
  - rule:
      actions:
        allow: 1
  denyall:
  - rule:
      actions:
        allow: 0
dps:
  # Need at least DP defined always.
  anchor:
    dp_id: 0x99
    hardware: Open vSwitch
    interfaces:
        1:
           native_vlan: 100
  rootsw:
    dp_id: 0x77
    hardware: Open vSwitch
    interfaces:
        1:
           native_vlan: 100
        88:
           output_only: true
  testnet:
    dp_id: 0x1
    hardware: Open vSwitch
    interfaces:
        0xfffffffe:
            native_vlan: 100
            opstatus_reconf: false
    interface_ranges:
        2-10:
            native_vlan: 100
            acls_in: [denyall]
EOFC

docker-compose build || exit 1
init_ovs

sudo ip link add rootswintp1 type veth peer name rootswextp1
sudo ip link set dev rootswintp1 up || exit 1
sudo ip link set dev rootswextp1 up || exit 1

docker exec -t $OVSID ovs-vsctl add-br rootsw || exit 1
docker exec -t $OVSID ovs-vsctl set-fail-mode rootsw secure
docker exec -t $OVSID ovs-vsctl set bridge rootsw other-config:datapath-id=0x77
docker exec -t $OVSID ovs-vsctl set-controller rootsw tcp:127.0.0.1:6653 tcp:127.0.0.1:6654
docker exec -t $OVSID ovs-vsctl add-port rootsw rootswintp1 -- set interface rootswintp1 ofport_request=7
docker exec -t $OVSID ovs-vsctl show

echo starting dovesnap infrastructure
FAUCET_PREFIX=$TMPDIR STACK_PRIORITY1=rootsw STACKING_INTERFACES=rootsw:7:rootswextp1 STACK_MIRROR_INTERFACE=rootsw:88 STACK_OFCONTROLLERS=tcp:127.0.0.1:6653,tcp:127.0.0.1:6654 docker-compose -f docker-compose.yml -f docker-compose-standalone.yml up -d || exit 1
wait_faucet

docker ps -a
echo creating testnet
docker network create testnet -d ovs --internal -o ovs.bridge.mode=nat -o ovs.bridge.dpid=0x1 -o ovs.bridge.lbport=99 || exit 1
docker network ls
restart_wait_dovesnap
echo creating testcon
# github test runner can't use ping.
docker pull busybox
docker run -d --label="dovesnap.faucet.portacl=ratelimitit" --label="dovesnap.faucet.mirror=true" --net=testnet --rm --name=testcon busybox sleep 1h
RET=$?
if [ "$RET" != "0" ] ; then
	echo testcon container creation returned: $RET
	exit 1
fi
wait_acl
wait_mirror
sudo grep -q "description: /testcon" $FAUCET_CONFIG || exit 1
echo verifying networking
docker exec -t testcon wget -q -O- bing.com || exit 1
OVSID="$(docker ps -q --filter name=ovs)"
echo showing packets tunnelled: tunnel 356 is vlan 100 plus default offset 256
PACKETS=$(docker exec -t $OVSID ovs-ofctl dump-flows rootsw table=0,dl_vlan=356|grep -v n_packets=0)
echo $PACKETS
if [ "$PACKETS" = "" ] ; then
        echo no packets were tunnelled
        exit 1
fi

clean_dirs

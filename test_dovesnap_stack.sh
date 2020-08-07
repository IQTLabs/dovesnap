#!/bin/bash

export TMPDIR=$(mktemp -d)
export FAUCET_CONFIG=$TMPDIR/etc/faucet/faucet.yaml
export GAUGE_CONFIG=$TMPDIR/etc/faucet/gauge.yaml
if [ ! -d "$TMPDIR" ] ; then
	exit 1
fi
mkdir -p $TMPDIR/etc/faucet

echo configuring faucet: $FAUCET_CONFIG

sudo rm -f $FAUCET_CONFIG
cat >$FAUCET_CONFIG <<EOFC || exit 1
acls:
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
cat >$GAUGE_CONFIG <<EOGC || exit 1
faucet_configs:
    - '/etc/faucet/faucet.yaml'
watchers:
    port_status_poller:
        type: 'port_state'
        all_dps: True
        db: 'prometheus'
    port_stats_poller:
        type: 'port_stats'
        all_dps: True
        interval: 30
        db: 'prometheus'
dbs:
    prometheus:
        type: 'prometheus'
        prometheus_addr: '0.0.0.0'
        prometheus_port: 9303
EOGC
echo creating keys
mkdir -p /opt/faucetconfrpc || exit 1
FAUCET_PREFIX=$TMPDIR docker-compose -f docker-compose.yml -f docker-compose-standalone.yml up faucet_certstrap || exit 1
ls -al /opt/faucetconfrpc/client.key || exit 1

docker-compose build || exit 1
docker-compose -f docker-compose.yml up -d ovs || exit 1

sudo ip link add rootswintp1 type veth peer name rootswextp1
sudo ip link set dev rootswintp1 up || exit 1
sudo ip link set dev rootswextp1 up || exit 1

OVSID="$(docker ps -q --filter name=ovs)"
while ! docker exec -t $OVSID ovs-vsctl show ; do
	echo waiting for OVS
	sleep 1
done
docker exec -t $OVSID /bin/sh -c 'for i in `ovs-vsctl list-br` ; do ovs-vsctl del-br $i ; done' || exit 1
docker exec -t $OVSID ovs-vsctl add-br rootsw || exit 1
docker exec -t $OVSID ovs-vsctl set-fail-mode rootsw secure
docker exec -t $OVSID ovs-vsctl set bridge rootsw other-config:datapath-id=0x77
docker exec -t $OVSID ovs-vsctl set-controller rootsw tcp:127.0.0.1:6653 tcp:127.0.0.1:6654
docker exec -t $OVSID ovs-vsctl add-port rootsw rootswintp1 -- set interface rootswintp1 ofport_request=7
docker exec -t $OVSID ovs-vsctl show

echo starting dovesnap infrastructure
FAUCET_PREFIX=$TMPDIR STACKING_INTERFACES=rootsw:7:rootswextp1 STACK_MIRROR_INTERFACE=99:666:rootsw:88 STACK_OFCONTROLLERS=tcp:127.0.0.1:6653,tcp:127.0.0.1:6654 docker-compose -f docker-compose.yml -f docker-compose-standalone.yml up -d || exit 1
for p in 6653 6654 ; do
	PORTCOUNT=""
	while [ "$PORTCOUNT" = "0" ] ; do
		echo waiting for $p
		PORTCOUNT=$(ss -tHl sport = :$p|grep -c $p)
		sleep 1
	done
done
docker ps -a
echo creating testnet
docker network create testnet -d ovs -o ovs.bridge.mode=nat -o ovs.bridge.dpid=0x1 || exit 1
docker network ls
echo creating testcon
# github test runner can't use ping.
docker pull busybox
docker run -d --label="dovesnap.faucet.portacl=allowall" --label="dovesnap.faucet.mirror=true" --net=testnet --rm --name=testcon busybox sleep 1h
RET=$?
if [ "$RET" != "0" ] ; then
	echo testcon container creation returned: $RET
	exit 1
fi
echo waiting for ACL to be applied
DOVESNAPID="$(docker ps -q --filter name=dovesnap_plugin)"
ACLCOUNT=0
while [ "$ACLCOUNT" != "2" ] ; do
	docker logs $DOVESNAPID
	sudo cat $FAUCET_CONFIG
	ACLCOUNT=$(sudo grep -c allowall $FAUCET_CONFIG)
        sleep 1
done
echo waiting for mirror to be applied
MIRRORCOUNT=0
while [ "$MIRRORCOUNT" != "1" ] ; do
        docker logs $DOVESNAPID
        sudo cat $FAUCET_CONFIG
        MIRRORCOUNT=$(sudo grep -c mirror: $FAUCET_CONFIG)
        sleep 1
done
sudo grep "description: /testcon" $FAUCET_CONFIG || exit 1
echo verifying networking
docker exec -t testcon wget -q -O- bing.com || exit 1
OVSID="$(docker ps -q --filter name=ovs)"
echo showing packets tunnelled
PACKETS=$(docker exec -t $OVSID ovs-ofctl dump-flows rootsw table=0,dl_vlan=666|grep -v n_packets=0)
echo $PACKETS
if [ "$PACKETS" = "" ] ; then
        echo no packets were tunnelled
        exit 1
fi
docker rm -f testcon || exit 1
docker network rm testnet || exit 1
FAUCET_PREFIX=$TMPDIR docker-compose -f docker-compose.yml -f docker-compose-standalone.yml stop
rm -rf $TMPDIR

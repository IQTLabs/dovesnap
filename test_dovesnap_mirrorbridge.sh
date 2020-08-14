#!/bin/bash

export TMPDIR=$(mktemp -d)
export MIRROR_PCAP=$TMPDIR/mirror.pcap
export FAUCET_CONFIG=$TMPDIR/etc/faucet/faucet.yaml
export GAUGE_CONFIG=$TMPDIR/etc/faucet/gauge.yaml
if [ ! -d "$TMPDIR" ] ; then
	exit 1
fi
mkdir -p $TMPDIR/etc/faucet

sudo ip link add mirrori type veth peer name mirroro
sudo ip link set dev mirrori up || exit 1
sudo ip link set dev mirroro up || exit 1

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
  testnet:
    dp_id: 0x1
    hardware: Open vSwitch
    interfaces:
        0xfffffffe:
            native_vlan: 100
            opstatus_reconf: false
    interface_ranges:
        1-10:
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
ls -al /opt/faucetconfrpc/faucetconfrpc.key || exit 1
echo starting dovesnap infrastructure
docker-compose build || exit 1
docker-compose -f docker-compose.yml up -d ovs || exit 1

OVSID="$(docker ps -q --filter name=ovs)"
while ! docker exec -t $OVSID ovs-vsctl show ; do
        echo waiting for OVS
        sleep 1
done
docker exec -t $OVSID /bin/sh -c 'for i in `ovs-vsctl list-br` ; do ovs-vsctl del-br $i ; done' || exit 1

FAUCET_PREFIX=$TMPDIR MIRROR_BRIDGE_OUT=mirrori docker-compose -f docker-compose.yml -f docker-compose-standalone.yml up -d || exit 1
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
docker network create testnet -d ovs -o ovs.bridge.mode=nat -o ovs.bridge.dpid=0x1 -o ovs.bridge.controller=tcp:127.0.0.1:6653,tcp:127.0.0.1:6654 || exit 1
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
sudo timeout 30s tcpdump -n -c 1 -U -i mirroro -w $MIRROR_PCAP tcp &
docker exec -t testcon wget -q -O- bing.com || exit 1
docker rm -f testcon || exit 1
docker network rm testnet || exit 1
sudo cat $FAUCET_CONFIG
FAUCET_PREFIX=$TMPDIR docker-compose -f docker-compose.yml -f docker-compose-standalone.yml stop
tcpdump -n -r $MIRROR_PCAP -v | grep TCP || exit 1
rm -rf $TMPDIR

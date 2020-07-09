#!/bin/bash

export TMPDIR=/tmp
export FAUCET_CONFIG=$TMPDIR/faucet.yaml

cat >$FAUCET_CONFIG <<EOC
acls:
  allowall:
  - rule:
      actions:
        allow: 1
dps:
  ovs:
    dp_id: 0x1
    hardware: Open vSwitch
    interfaces:
        0xfffffffe:
            native_vlan: 100
            opstatus_reconf: false
    interface_ranges:
        1-10:
            native_vlan: 100
EOC

mkdir -p /opt/dovesnap || exit 1
docker-compose build && FAUCET_CONFIG_DIR=$TMPDIR docker-compose -f docker-compose.yml -f docker-compose-standalone.yml up -d || exit 1
wget --retry-connrefused --tries=20 -q -O/dev/null localhost:9302 > /dev/null || exit 1
docker network create testnet -d ovs -o ovs.bridge.mode=nat -o ovs.bridge.dpid=0x1 -o ovs.bridge.controller=tcp:127.0.0.1:6653 || exit 1
DPSTATUS=""
while [ "$DPSTATUS" == "" ] ; do
	sleep 1
	DPSTATUS=$(wget -q -O- localhost:9302|grep -E "^dp_status"|grep -E "1.0$")
	echo $DPSTATUS
done
# github test runner can't use ping.
docker run -t --net=testnet --rm busybox wget -q -O- bing.com || exit 1
docker network rm testnet || exit 1
FAUCET_CONFIG_DIR=$TMPDIR docker-compose -f docker-compose.yml -f docker-compose-standalone.yml stop

#!/bin/bash

export TMPDIR=/tmp
export FAUCET_CONFIG=$TMPDIR/faucet.yaml
export FAUCET_LOG=$TMPDIR/faucet.log
export FAUCET_EXCEPTION_LOG=$TMPDIR/faucet_exception.log

cat >$FAUCET_CONFIG <<EOC
dps:
  ovs:
    dp_id: 0x1
    hardware: Open vSwitch
    interfaces:
        0xfffffffe:
            native_vlan: 100
    interface_ranges:
        1-10:
            native_vlan: 100
EOC

nohup faucet &
FAUCETPID=$!

docker-compose build && docker-compose up -d || exit 1
docker network create testnet -d ovs -o ovs.bridge.mode=nat -o ovs.bridge.dpid=0x1 -o ovs.bridge.controller=tcp:127.0.0.1:6653 || exit 1
docker run -it --net=testnet --rm busybox ping -c3 google.com || exit 1
docker network rm testnet || exit 1
docker-compose stop

kill $FAUCETPID

#!/bin/bash

source ./tests/lib_test.sh

sudo ip link add mirrori type veth peer name mirroro
sudo ip link set dev mirrori up || exit 1
sudo ip link set dev mirroro up || exit 1
sudo ip link add addport1 type veth peer name addport2 && true
sudo ip link set dev addport1 up
sudo ip link set dev addport2 up

init_dirs
conf_faucet
conf_gauge
conf_keys

ULF=$TMPDIR/udhcpd.leases
UCF=$TMPDIR/udhcpd.conf
UCP=$TMPDIR/udhcpd.pid
sudo rm -f $ULF
touch $ULF

cat << EOF > $UCF
start           100.64.0.2
end             100.64.0.5
lease_file      $ULF
interface       addport2
max_leases	2
pidfile		$UCP
EOF

sudo /usr/sbin/udhcpd -I 100.64.0.1 -S $UCF

echo starting dovesnap infrastructure
docker-compose build || exit 1
init_ovs

FAUCET_PREFIX=$TMPDIR MIRROR_BRIDGE_OUT=mirrori docker-compose -f docker-compose.yml -f docker-compose-standalone.yml up -d || exit 1
wait_faucet

docker ps -a
echo creating testnet
docker network create testnet -d ovs --internal --ipam-driver null -o ovs.bridge.add_ports=addport1 -o ovs.bridge.dhcp=true -o ovs.bridge.mode=flat -o ovs.bridge.dpid=0x1 -o ovs.bridge.controller=tcp:127.0.0.1:6653,tcp:127.0.0.1:6654 -o ovs.bridge.preallocate_ports=10 || exit 1
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
wait_mirror 1
sudo grep -q "description: /testcon" $FAUCET_CONFIG || exit 1
echo verifying networking
wait_for_container_ip 100.64.0
wait_for_status_container_ip 100.64.0
docker restart testcon
wait_for_container_ip 100.64.0
wait_for_status_container_ip 100.64.0

sudo kill $(cat $UCP)

clean_dirs

exit 0

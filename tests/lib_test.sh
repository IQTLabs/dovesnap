#!/bin/bash

reset_ovsid ()
{
        OVSID="$(docker ps -q --filter name=ovs)"
}

reset_bridgename ()
{
        BRIDGE=$(docker exec -t $OVSID ovs-vsctl list-br|grep -Eo 'ovsbr\S+')
}

restart_dovesnap ()
{
        echo restarting dovesnap
        DOVESNAPID="$(docker ps -q --filter name=dovesnap_plugin)"
        docker logs $DOVESNAPID
        docker restart $DOVESNAPID
        docker logs $DOVESNAPID
}

restart_wait_dovesnap ()
{
        echo waiting for FAUCET config to have testnet mirror port
        TESTNETCOUNT=0
        while [ "$TESTNETCOUNT" != "1" ] ; do
                TESTNETCOUNT=$(sudo grep -c 99: $FAUCET_CONFIG)
                sleep 1
        done
        restart_dovesnap
}

init_dirs()
{
        export TMPDIR=$(mktemp -d)
        export FAUCET_CONFIG=$TMPDIR/etc/faucet/faucet.yaml
        export GAUGE_CONFIG=$TMPDIR/etc/faucet/gauge.yaml
        if [ ! -d "$TMPDIR" ] ; then
                exit 1
        fi
        mkdir -p $TMPDIR/etc/faucet
        MIRROR_PCAP=$TMPDIR/mirror.cap
}

clean_dirs()
{
        wget -q -O- localhost:9401/networks || exit 1
        sudo ./graph_dovesnap/graph_dovesnap -o /tmp/dovesnapviz || exit 1
        docker rm -f testcon || exit 1
        docker network rm testnet || exit 1
        sleep 2
        FAUCET_PREFIX=$TMPDIR docker-compose -f docker-compose.yml -f docker-compose-standalone.yml stop
        rm -rf $TMPDIR
}

conf_faucet()
{
        echo configuring faucet
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
}

conf_gauge()
{
        echo configuring gauge
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
}

conf_keys ()
{
        echo creating keys
        mkdir -p /opt/faucetconfrpc || exit 1
        FAUCET_PREFIX=$TMPDIR docker-compose -f docker-compose.yml -f docker-compose-standalone.yml up faucet_certstrap || exit 1
        ls -al /opt/faucetconfrpc/faucetconfrpc.key || exit 1
}

wait_faucet ()
{
        for p in 6653 6654 9302 ; do
                PORTCOUNT=""
                while [ "$PORTCOUNT" = "0" ] ; do
                        echo waiting for $p
                        PORTCOUNT=$(ss -tHl sport = :$p|grep -c $p)
                        sleep 1
                done
        done
}

wait_acl ()
{
        echo waiting for ACL to be applied
        DOVESNAPID="$(docker ps -q --filter name=dovesnap_plugin)"
        ACLCOUNT=0
        while [ "$ACLCOUNT" != "2" ] ; do
                docker logs $DOVESNAPID
                sudo cat $FAUCET_CONFIG
                ACLCOUNT=$(sudo grep -c ratelimitit $FAUCET_CONFIG)
                sleep 1
        done
        reset_ovsid
        reset_bridgename
        OUTPUT=""
        while [ "$OUTPUT" != "meter" ] ; do
                OUTPUT=$(docker exec -t $OVSID ovs-ofctl dump-flows -OOpenFlow13 $BRIDGE table=0|grep -o meter|cat)
                echo waiting for meter flow in table 0
                sleep 1
        done
}

wait_stack_state ()
{
        state=$1
        count=$2
        FIP=$(docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' dovesnap_faucet_1)
        STACKUPCOUNT=0
        echo waiting for $count stack ports to be state $state
        while [ "$STACKUPCOUNT" != "$count" ] ; do
                STACKUPCOUNT=$(wget -q -O- $FIP:9302|grep -Ec "^port_stack_state.+$state.0$")
                sleep 1
        done
        sleep 5
}

wait_tunnel_src ()
{
        table=$1
        inport=$2
        reset_ovsid
        reset_bridgename
        OUTPUT=""
        while [ "$OUTPUT" != "push_vlan:" ] ; do
                OUTPUT=$(docker exec -t $OVSID ovs-ofctl dump-flows -OOpenFlow13 $BRIDGE in_port=$inport,table=$table|grep -o push_vlan:|cat)
                echo waiting for tunnel source rule for port $inport in $table
                sleep 1
        done
}

wait_mirror ()
{
        table=$1
        if [ "$table" == "" ] ; then
                table=0
        fi
        echo waiting for mirror to be applied to config
        DOVESNAPID="$(docker ps -q --filter name=dovesnap_plugin)"
        MIRRORCOUNT=0
        while [ "$MIRRORCOUNT" != "1" ] ; do
                docker logs $DOVESNAPID
                sudo cat $FAUCET_CONFIG
                MIRRORCOUNT=$(sudo grep -c mirror: $FAUCET_CONFIG)
                sleep 1
        done
        reset_ovsid
        reset_bridgename
        OUTPUT=""
        while [ "$OUTPUT" != "output:" ] ; do
                OUTPUT=$(docker exec -t $OVSID ovs-ofctl dump-flows -OOpenFlow13 $BRIDGE table=$table|grep -o output:|cat)
                echo waiting for mirror flow in table $table
                sleep 1
        done
}

init_ovs ()
{
        docker-compose -f docker-compose.yml up -d ovs || exit 1
        reset_ovsid
        while ! docker exec -t $OVSID ovs-vsctl show ; do
                echo waiting for OVS
                sleep 1
        done
        docker exec -t $OVSID /bin/sh -c 'for i in `ovs-vsctl list-br` ; do ovs-vsctl del-br $i ; done' || exit 1
}

wait_for_pcap_match ()
{
        i=0
        OUT=""
        sudo chmod go+rx $TMPDIR
        while [ "$OUT" == "" ] && [ "$i" != 30 ] ; do
                echo waiting for pcap match $PCAPMATCH: $i
                sudo chown root $MIRROR_PCAP
                OUT=$(sudo tcpdump -n -r $MIRROR_PCAP -v | grep $PCAPMATCH)
                ((i=i+1))
                sleep 1
        done
        if [ "$OUT" == "" ] ; then
                echo $PCAPMATCH not found in pcap
                exit 1
        fi
        echo $PCAPMATCH
}

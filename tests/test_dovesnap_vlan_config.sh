#!/bin/bash
# tests/test_dovesnap_port_vlans.sh - Test per-port native_vlan configuration

source ./tests/lib_test.sh

echo "=========================================="
echo "Testing Per-Port VLAN Configuration"
echo "=========================================="

# Create test veth pairs for physical ports
echo "Creating test veth pairs..."
sudo ip link add odstest1 type veth peer name odstest1-peer || true
sudo ip link add odstest2 type veth peer name odstest2-peer || true
sudo ip link add odstest3 type veth peer name odstest3-peer || true
sudo ip link set dev odstest1 up || exit 1
sudo ip link set dev odstest2 up || exit 1
sudo ip link set dev odstest3 up || exit 1
sudo ip link set dev odstest1-peer up || exit 1
sudo ip link set dev odstest2-peer up || exit 1
sudo ip link set dev odstest3-peer up || exit 1

init_dirs
conf_faucet
conf_gauge
conf_keys

echo "Starting dovesnap infrastructure"
docker compose build || exit 1
init_ovs

# Start faucet services first to get the network and IP
FAUCET_PREFIX=$TMPDIR docker compose -f docker-compose-standalone.yml up -d faucet gauge faucetconfrpc || exit 1

# Wait a moment for containers to start and get IPs
sleep 3

# Get the IP of faucetconfrpc container on the dovesnap network
FAUCETCONFRPC_IP=$(docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' dovesnap-faucetconfrpc-1)
if [ -z "$FAUCETCONFRPC_IP" ]; then
    echo "ERROR: Could not get faucetconfrpc IP"
    clean_dirs
    exit 1
fi
echo "faucetconfrpc IP: $FAUCETCONFRPC_IP"

# Now start the plugin with the correct IP
FAUCET_PREFIX=$TMPDIR FAUCETCONFRPC_IP=$FAUCETCONFRPC_IP docker compose -f docker-compose.yml -f docker-compose-standalone.yml up -d || exit 1
wait_faucet

docker ps -a

echo "Creating testnet with per-port VLANs"
echo "  odstest1/1  -> VLAN 20"
echo "  odstest2/2  -> VLAN 30"
echo "  odstest3/3  -> VLAN 40"

docker network create testnet -d dovesnap \
  --internal \
  -o ovs.bridge.mode=nat \
  -o ovs.bridge.dpid=0x1 \
  -o ovs.bridge.vlan=100 \
  -o ovs.bridge.controller=tcp:127.0.0.1:6653,tcp:127.0.0.1:6654 \
  -o ovs.bridge.add_ports=odstest1/1//20,odstest2/2//30,odstest3/3//40 \
  -o ovs.bridge.preallocate_ports=10 || exit 1

docker network ls

echo "Waiting for configuration to be applied..."
sleep 3

echo ""
echo "Verifying VLAN configuration in faucet.yaml..."
echo "=========================================="

# Check if the configuration file exists
if [ ! -f "$FAUCET_CONFIG" ]; then
    echo "ERROR: Faucet config file not found at $FAUCET_CONFIG"
    clean_dirs
    exit 1
fi

# Fix permissions to read the config file
sudo chmod -R a+r $TMPDIR/etc/faucet/ || true

# Display the testnet configuration
echo "Testnet configuration from faucet.yaml:"
grep -A 30 "testnet:" $FAUCET_CONFIG || {
    echo "ERROR: testnet not found in faucet config"
    clean_dirs
    exit 1
}

echo ""
echo "Verifying port VLAN assignments..."
VLAN_CHECK_FAILED=0

# Display faucet configuration file for reference
echo "Faucet configuration file ($FAUCET_CONFIG):"
cat $FAUCET_CONFIG
echo ""
# Check port 1 has VLAN 20
if grep -A 3 "^      1:" $FAUCET_CONFIG | grep -q "native_vlan: 20"; then
    echo "✓ Port 1 (odstest1): VLAN 20 configured"
else
    echo "✗ Port 1 (odstest1): VLAN 20 NOT found"
    VLAN_CHECK_FAILED=1
fi

# Check port 2 has VLAN 30
if grep -A 3 "^      2:" $FAUCET_CONFIG | grep -q "native_vlan: 30"; then
    echo "✓ Port 2 (odstest2): VLAN 30 configured"
else
    echo "✗ Port 2 (odstest2): VLAN 30 NOT found"
    VLAN_CHECK_FAILED=1
fi

# Check port 3 has VLAN 40
if grep -A 3 "^      3:" $FAUCET_CONFIG | grep -q "native_vlan: 40"; then
    echo "✓ Port 3 (odstest3): VLAN 40 configured correctly"
else
    echo "✗ Port 3 (odstest3): VLAN 40 NOT found"
    VLAN_CHECK_FAILED=1
fi

# Verify preallocated ports use default VLAN 100
echo ""
echo "Verifying preallocated ports use default VLAN 100..."
PREALLOCATED_CHECK_FAILED=0

for port in 11 12 13; do
    if grep -A 3 "^      $port:" $FAUCET_CONFIG | grep -q "native_vlan: 100"; then
        echo "✓ Port $port (preallocated): VLAN 100 (default) configured correctly"
    else
        echo "✗ Port $port (preallocated): VLAN 100 NOT found"
        PREALLOCATED_CHECK_FAILED=1
    fi
done

echo ""
echo "Testing container creation on custom VLAN port..."
# Create a container to verify the network works
docker pull busybox
docker run -d --net=testnet --rm --name=testcon busybox sleep 1h
RET=$?
if [ "$RET" != "0" ]; then
    echo "✗ testcon container creation returned: $RET"
    clean_dirs
    exit 1
fi
echo "✓ Container created successfully"

wait_testcon

echo ""
echo "Cleaning up test interfaces..."
sudo ip link del odstest1 2>/dev/null || true
sudo ip link del odstest2 2>/dev/null || true
sudo ip link del odstest3 2>/dev/null || true

clean_dirs

echo ""
echo "=========================================="
if [ $VLAN_CHECK_FAILED -eq 0 ] && [ $PREALLOCATED_CHECK_FAILED -eq 0 ]; then
    echo "✓ TEST PASSED: All port VLANs configured correctly!"
    exit 0
else
    echo "✗ TEST FAILED: VLAN configuration errors detected"
    exit 1
fi
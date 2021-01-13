package ovs

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	networkplugin "github.com/docker/go-plugins-helpers/network"
	log "github.com/sirupsen/logrus"
)

const (
	DriverName = "ovs"

	genericOption  = "com.docker.network.generic"
	internalOption = "com.docker.network.internal"
	portMapOption  = "com.docker.network.portmap"

	bindInterfaceOption = "ovs.bridge.bind_interface"
	bridgeAddPorts      = "ovs.bridge.add_ports"
	bridgeController    = "ovs.bridge.controller"
	bridgeDpid          = "ovs.bridge.dpid"
	bridgeLbPort        = "ovs.bridge.lbport"
	bridgeNameOption    = "ovs.bridge.name"
	dhcpOption          = "ovs.bridge.dhcp"
	mirrorTunnelVid     = "ovs.bridge.mirror_tunnel_vid"
	modeOption          = "ovs.bridge.mode"
	NATAclOption        = "ovs.bridge.nat_acl"
	mtuOption           = "ovs.bridge.mtu"
	vlanOption          = "ovs.bridge.vlan"
	userspaceOption     = "ovs.bridge.userspace"
	ovsLocalMacOption   = "ovs.bridge.ovs_local_mac"

	defaultLbPort           = 99
	defaultMTU              = 1500
	defaultMode             = modeFlat
	defaultRoute            = "0.0.0.0/0"
	defaultTunnelVLANOffset = 256
	defaultVLAN             = 100

	modeFlat = "flat"
	modeNAT  = "nat"

	bridgePrefix             = "ovsbr-"
	containerEthName         = "eth"
	mirrorBridgeName         = "mirrorbr"
	netNsPath                = "/var/run/netns"
	ofPortLocal       uint32 = 4294967294
	ovsPortPrefix            = "ovs-veth0-"
	patchPrefix              = "ovp"
	peerOvsPortPrefix        = "ethc"
	stackDpidPrefix          = "0x0E0F00"
	ovsStartupRetries        = 5
	dockerRetries            = 3
)

var (
	validModes = map[string]bool{
		modeNAT:  true,
		modeFlat: true,
	}
)

type StackMirrorConfig struct {
	LbPort           uint32
	TunnelVid        uint32
	RemoteDpName     string
	RemoteMirrorPort uint32
}

func mustGetInternalOption(r *networkplugin.CreateNetworkRequest) bool {
	if r.Options == nil {
		return false
	}
	return r.Options[internalOption].(bool)
}

func getGenericOption(r *networkplugin.CreateNetworkRequest, optionName string) string {
	if r.Options == nil {
		return ""
	}
	optionsMap, have_options := r.Options[genericOption].(map[string]interface{})
	if !have_options {
		return ""
	}
	optionValue, have_option := optionsMap[optionName].(string)
	if !have_option {
		return ""
	}
	return optionValue
}

func getGenericUintOption(r *networkplugin.CreateNetworkRequest, optionName string, defaultOption uint) uint {
	option := getGenericOption(r, optionName)
	return uint(defaultUint(option, uint64(defaultOption)))
}

func mustGetTunnelVid(r *networkplugin.CreateNetworkRequest) uint {
	return getGenericUintOption(r, mirrorTunnelVid, mustGetBridgeVLAN(r)+defaultTunnelVLANOffset)
}

func mustGetBridgeMTU(r *networkplugin.CreateNetworkRequest) uint {
	return getGenericUintOption(r, mtuOption, defaultMTU)
}

func mustGetLbPort(r *networkplugin.CreateNetworkRequest) uint {
	return getGenericUintOption(r, bridgeLbPort, defaultLbPort)
}

func mustGetBridgeName(r *networkplugin.CreateNetworkRequest) string {
	bridgeName := bridgePrefix + truncateID(r.NetworkID)
	name := getGenericOption(r, bridgeNameOption)
	if name != "" {
		bridgeName = name
	}
	return bridgeName
}

func mustGetOvsLocalMac(r *networkplugin.CreateNetworkRequest) string {
	return getGenericOption(r, ovsLocalMacOption)
}

func mustGetBridgeMode(r *networkplugin.CreateNetworkRequest) string {
	bridgeMode := defaultMode
	mode := getGenericOption(r, modeOption)
	if mode != "" {
		if _, isValid := validModes[mode]; !isValid {
			panic(fmt.Errorf("%s is not a valid mode", mode))
		}
		bridgeMode = mode
	}
	return bridgeMode
}

func mustGetBridgeController(r *networkplugin.CreateNetworkRequest) string {
	return getGenericOption(r, bridgeController)
}

func mustGetBridgeDpid(r *networkplugin.CreateNetworkRequest) string {
	return getGenericOption(r, bridgeDpid)
}

func mustGetBridgeVLAN(r *networkplugin.CreateNetworkRequest) uint {
	return getGenericUintOption(r, vlanOption, defaultVLAN)
}

func mustGetBridgeAddPorts(r *networkplugin.CreateNetworkRequest) string {
	return getGenericOption(r, bridgeAddPorts)
}

func mustGetNATAcl(r *networkplugin.CreateNetworkRequest) string {
	return getGenericOption(r, NATAclOption)
}

func mustGetGatewayIPFromData(data []*networkplugin.IPAMData) string {
	if len(data) > 0 {
		if data[0] != nil {
			if data[0].Gateway != "" {
				return data[0].Gateway
			}
		}
	}
	return ""
}

func mustGetGatewayIP(r *networkplugin.CreateNetworkRequest) (string, string) {
	// Guess gateway IP, prefer IPv4.
	ipv6Gw := mustGetGatewayIPFromData(r.IPv6Data)
	ipv4Gw := mustGetGatewayIPFromData(r.IPv4Data)
	gatewayIP := ipv6Gw
	if ipv4Gw != "" {
		gatewayIP = ipv4Gw
	}
	if gatewayIP == "" {
		return "", ""
	}
	parts := strings.Split(gatewayIP, "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1]
	}
	panic(fmt.Errorf("Cannot parse gateway IP: %s", gatewayIP))
}

func mustGetBindInterface(r *networkplugin.CreateNetworkRequest) string {
	return getGenericOption(r, bindInterfaceOption)
}

func parseBool(optionVal string) bool {
	boolVal, err := strconv.ParseBool(optionVal)
	if err != nil {
		return false
	}
	return boolVal
}

func mustGetUseDHCP(r *networkplugin.CreateNetworkRequest) bool {
	return parseBool(getGenericOption(r, dhcpOption))
}

func mustGetUserspace(r *networkplugin.CreateNetworkRequest) bool {
	return parseBool(getGenericOption(r, userspaceOption))
}

func truncateID(id string) string {
	return id[:5]
}

func (d *Driver) mustGetStackBridgeLink() (string, uint32) {
	return d.stackMirrorInterface[0], uint32(defaultUint(d.stackMirrorInterface[1], 0))
}

func (d *Driver) getStackMirrorConfig(r *networkplugin.CreateNetworkRequest) StackMirrorConfig {
	lbPort := mustGetLbPort(r)
	var tunnelVid uint = 0
	remoteDpName := ""
	var mirrorPort uint32 = 0

	if usingStackMirroring(d) {
		tunnelVid = mustGetTunnelVid(r)
		remoteDpName, mirrorPort = d.mustGetStackBridgeLink()
	}

	return StackMirrorConfig{
		LbPort:           uint32(lbPort),
		TunnelVid:        uint32(tunnelVid),
		RemoteDpName:     remoteDpName,
		RemoteMirrorPort: uint32(mirrorPort),
	}
}

func (d *Driver) getShortEngineID() (string, error) {
	info, err := d.dockerer.client.Info(context.Background())
	if err != nil {
		return "", err
	}
	log.Debugf("Docker Engine ID %s:", info.ID)
	engineId := base36to16(strings.Split(info.ID, ":")[0])
	return engineId, nil
}

func (d *Driver) getStackDP() (string, string, error) {
	engineId, err := d.getShortEngineID()
	if err != nil {
		return "", "", err
	}
	dpid := stackDpidPrefix + engineId
	dpName := "dovesnap" + engineId
	return dpid, dpName, nil
}

func (d *Driver) mustGetLoopbackDP() string {
	engineId, _ := d.getShortEngineID()
	return "lb" + engineId
}

func (d *Driver) mustGetStackingInterface(stackingInterface string) (string, uint32, string) {
	stackSlice := strings.Split(stackingInterface, ":")
	remoteDP := stackSlice[0]
	remotePort, err := ParseUint32(stackSlice[1])
	if err != nil {
		panic(fmt.Errorf("Unable to convert remote port to an unsigned integer because: [ %s ]", err))
	}
	localInterface := stackSlice[2]
	if err != nil {
		panic(fmt.Errorf("Unable to convert local port to an unsigned integer because: [ %s ]", err))
	}
	return remoteDP, uint32(remotePort), localInterface
}

func (d *Driver) mustGetStackBridgeConfig() (string, string, uint64, string) {
	dpid, dpName, err := d.getStackDP()
	if err != nil {
		panic(err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		panic(err)
	}

	uintDpid := mustGetUintFromHexStr(dpid)
	return hostname, dpid, uintDpid, dpName
}

func mustGetBridgeNameFromResource(r *types.NetworkResource) string {
	return bridgePrefix + truncateID(r.ID)
}

func getStrOptionFromResource(r *types.NetworkResource, optionName string, defaultOptionValue string) string {
	if r.Options == nil {
		return defaultOptionValue
	}
	optionValue, have_option := r.Options[optionName]
	if !have_option {
		return defaultOptionValue
	}
	return optionValue
}

func getUintOptionFromResource(r *types.NetworkResource, optionName string, defaultOptionValue uint) uint {
	optionStrValue := getStrOptionFromResource(r, optionName, "")
	return uint(defaultUint(optionStrValue, uint64(defaultOptionValue)))
}

func mustGetBridgeDpidFromResource(r *types.NetworkResource) (string, uint64) {
	dpid := getStrOptionFromResource(r, bridgeDpid, "")
	uintDpid := mustGetUintFromHexStr(dpid)
	return dpid, uintDpid
}

func getGatewayFromResource(r *types.NetworkResource) (string, string) {
	if len(r.IPAM.Config) > 0 {
		config := r.IPAM.Config[0]
		subnetIP := config.Subnet
		parts := strings.Split(subnetIP, "/")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return config.Gateway, parts[1]
		}
	}
	return "", ""
}

func getNetworkStateFromResource(r *types.NetworkResource) (NetworkState, error) {
	var err error = nil
	ns := NetworkState{}
	defer func() {
		if rerr := recover(); rerr != nil {
			err = fmt.Errorf("missing bridge info: %v", rerr)
		}
	}()
	dpid, uintDpid := mustGetBridgeDpidFromResource(r)
	gateway, mask := getGatewayFromResource(r)
	ns = NetworkState{
		NetworkName:       r.Name,
		BridgeName:        mustGetBridgeNameFromResource(r),
		BridgeDpid:        dpid,
		BridgeDpidUint:    uintDpid,
		BridgeVLAN:        getUintOptionFromResource(r, vlanOption, defaultVLAN),
		MTU:               getUintOptionFromResource(r, mtuOption, defaultMTU),
		Mode:              getStrOptionFromResource(r, modeOption, defaultMode),
		FlatBindInterface: getStrOptionFromResource(r, bindInterfaceOption, ""),
		AddPorts:          getStrOptionFromResource(r, bridgeAddPorts, ""),
		UseDHCP:           parseBool(getStrOptionFromResource(r, dhcpOption, "")),
		Userspace:         parseBool(getStrOptionFromResource(r, userspaceOption, "")),
		Gateway:           gateway,
		GatewayMask:       mask,
		NATAcl:            getStrOptionFromResource(r, NATAclOption, ""),
		OvsLocalMac:       getStrOptionFromResource(r, ovsLocalMacOption, ""),
		Controller:        getStrOptionFromResource(r, bridgeController, ""),
		Containers:        make(map[string]ContainerState),
		ExternalPorts:     make(map[string]ExternalPortState),
	}
	return ns, err
}

func (d *Driver) getStackMirrorConfigFromResource(r *types.NetworkResource) StackMirrorConfig {
	lbPort := getUintOptionFromResource(r, bridgeLbPort, defaultLbPort)
	var tunnelVid uint = 0
	remoteDpName := ""
	var mirrorPort uint32 = 0

	if usingStackMirroring(d) {
		vlan := getUintOptionFromResource(r, vlanOption, defaultVLAN)
		tunnelVid = getUintOptionFromResource(r, mirrorTunnelVid, vlan+defaultTunnelVLANOffset)
		remoteDpName, mirrorPort = d.mustGetStackBridgeLink()
	}

	return StackMirrorConfig{
		LbPort:           uint32(lbPort),
		TunnelVid:        uint32(tunnelVid),
		RemoteDpName:     remoteDpName,
		RemoteMirrorPort: uint32(mirrorPort),
	}
}

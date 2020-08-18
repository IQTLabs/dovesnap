package ovs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	networkplugin "github.com/docker/go-plugins-helpers/network"
	"github.com/iqtlabs/faucetconfrpc/faucetconfrpc"
	bc "github.com/kenshaw/baseconv"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	DriverName              = "ovs"
	defaultRoute            = "0.0.0.0/0"
	ovsPortPrefix           = "ovs-veth0-"
	peerOvsPortPrefix       = "ethc"
	bridgePrefix            = "ovsbr-"
	containerEthName        = "eth"
	bridgeLbPort            = "ovs.bridge.lbport"
	defaultLbPort           = 99
	mirrorTunnelVid         = "ovs.bridge.mirror_tunnel_vid"
	defaultTunnelVLANOffset = 256
	bridgeAddPorts          = "ovs.bridge.add_ports"
	bridgeDpid              = "ovs.bridge.dpid"
	bridgeController        = "ovs.bridge.controller"
	vlanOption              = "ovs.bridge.vlan"
	mtuOption               = "ovs.bridge.mtu"
	dhcpOption              = "ovs.bridge.dhcp"
	defaultMTU              = 1500
	defaultVLAN             = 100
	bridgeNameOption        = "ovs.bridge.name"
	bindInterfaceOption     = "ovs.bridge.bind_interface"
	modeOption              = "ovs.bridge.mode"
	modeNAT                 = "nat"
	modeFlat                = "flat"
	defaultMode             = modeFlat
	ovsStartupRetries       = 5
	dockerRetries           = 3
	stackDpidPrefix         = "0x0E0F00"
	ofPortLocal             = 4294967294
	mirrorBridgeName        = "mirrorbr"
	netNsPath		= "/var/run/netns"
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

type OFPortMap struct {
	OFPort     uint
	AddPorts   string
	Mode       string
	NetworkID  string
	EndpointID string
	Operation  string
}

type StackingPort struct {
	OFPort     uint
	RemoteDP   string
	RemotePort uint64
}

type OFPortContainer struct {
	OFPort           uint
	containerInspect types.ContainerJSON
}

type Driver struct {
	dockerclient *client.Client
	ovsdber
	faucetclient            faucetconfserver.FaucetConfServerClient
	stackPriority1          string
	stackingInterfaces      []string
	stackMirrorInterface    []string
	stackDefaultControllers string
	mirrorBridgeIn          string
	mirrorBridgeOut         string
	networks                map[string]*NetworkState
	ofportmapChan           chan OFPortMap
	stackMirrorConfigs      map[string]StackMirrorConfig
}

// NetworkState is filled in at network creation time
// it contains state that we wish to keep for each network
type NetworkState struct {
	NetworkName       string
	BridgeName        string
	BridgeDpid        string
	BridgeVLAN        int
	MTU               int
	Mode              string
	Gateway           string
	GatewayMask       string
	FlatBindInterface string
}

func setFaucetConfigFile(confclient faucetconfserver.FaucetConfServerClient, config_yaml string) {
	req := &faucetconfserver.SetConfigFileRequest{
		ConfigYaml: config_yaml,
		Merge:      true,
	}
	_, err := confclient.SetConfigFile(context.Background(), req)
	if err != nil {
		panic(err)
	}
}

func (d *Driver) getStackMirrorConfig(r *networkplugin.CreateNetworkRequest) StackMirrorConfig {
	lbPort := mustGetLbPort(r)
	tunnelVid := 0
	remoteDpName := ""
	mirrorPort := 0

	if usingStackMirroring(d) {
		tunnelVid = mustGetTunnelVid(r)
		remoteDpName = d.stackMirrorInterface[0]
		mirrorPort, _ = strconv.Atoi(d.stackMirrorInterface[1])
	}

	return StackMirrorConfig{
		LbPort:           uint32(lbPort),
		TunnelVid:        uint32(tunnelVid),
		RemoteDpName:     remoteDpName,
		RemoteMirrorPort: uint32(mirrorPort),
	}
}

func parseBool(optionVal string) bool {
	boolVal, err := strconv.ParseBool(optionVal)
	if err != nil {
		return false
	}
	return boolVal
}

func getGenericOption(r *networkplugin.CreateNetworkRequest, optionName string) string {
	if r.Options == nil {
		return ""
	}
	optionsMap, have_options := r.Options["com.docker.network.generic"].(map[string]interface{})
	if !have_options {
		return ""
	}
	optionValue, have_option := optionsMap[optionName].(string)
	if !have_option {
		return ""
	}
	return optionValue
}

func base36to16(value string) string {
	converted, _ := bc.Convert(strings.ToLower(value), bc.Digits36, bc.DigitsHex)
	digits := len(converted)
	for digits < 6 {
		converted = "0" + converted
		digits = len(converted)
	}
	return strings.ToUpper(converted)
}

func (d *Driver) getShortEngineID() (string, error) {
	info, err := d.dockerclient.Info(context.Background())
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

func (d *Driver) createLoopbackBridge() error {
	bridgeName := d.mustGetLoopbackDP()
	_, err := d.ovsdber.addBridgeExists(bridgeName)
	if err != nil {
		return err
	}
	err = d.ovsdber.makeLoopbackBridge(bridgeName)
	if err != nil {
		return err
	}
	return nil
}

func (d *Driver) mustGetStackingInterface(stackingInterface string) (string, uint64, string) {
	stackSlice := strings.Split(stackingInterface, ":")
	remoteDP := stackSlice[0]
	remotePort, err := strconv.ParseUint(stackSlice[1], 10, 32)
	if err != nil {
		panic(fmt.Errorf("Unable to convert remote port to an unsigned integer because: [ %s ]", err))
	}
	localInterface := stackSlice[2]
	if err != nil {
		panic(fmt.Errorf("Unable to convert local port to an unsigned integer because: [ %s ]", err))
	}
	return remoteDP, remotePort, localInterface
}

func (d *Driver) mustGetStackBridgeConfig() (string, string, int, string) {
	dpid, dpName, err := d.getStackDP()
	if err != nil {
		panic(err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		panic(err)
	}

	strDpid, _ := bc.Convert(strings.ToLower(dpid[2:]), bc.DigitsHex, bc.DigitsDec)
	intDpid, err := strconv.Atoi(strDpid)
	if err != nil {
		panic(fmt.Errorf("Unable convert dp_id to an int because: %v", err))
	}
	return hostname, dpid, intDpid, dpName
}

func (d *Driver) createMirrorBridge() {
	_, err := d.ovsdber.bridgeExists(mirrorBridgeName)
	if err == nil {
		log.Debugf("mirror bridge already exists")
		return
	}
	log.Debugf("creating mirror bridge")
	add_ports := d.mirrorBridgeOut
	if len(d.mirrorBridgeIn) > 0 {
		add_ports += "," + d.mirrorBridgeIn
	}
	err = d.ovsdber.createBridge(mirrorBridgeName, "", "", add_ports, true)
	if err != nil {
		panic(err)
	}
	d.ovsdber.makeMirrorBridge(mirrorBridgeName, 1)
}

func (d *Driver) createStackingBridge() error {
	hostname, dpid, intDpid, dpName := d.mustGetStackBridgeConfig()
	if d.stackDefaultControllers == "" {
		panic(fmt.Errorf("default OF controllers must be defined for stacking"))
	}

	// check if the stacking bridge already exists
	_, err := d.ovsdber.bridgeExists(dpName)
	if err == nil {
		log.Debugf("Stacking bridge already exists for this host")
		return nil
	} else {
		log.Infof("Stacking bridge doesn't exist, creating one now")
	}

	err = d.ovsdber.createBridge(dpName, d.stackDefaultControllers, dpid, "", true)
	if err != nil {
		log.Errorf("Unable to create stacking bridge because: [ %s ]", err)
	}

	// loop through stacking interfaces
	stackingPorts := []StackingPort{}
	stackingConfig := "{dps: {"
	for _, stackingInterface := range d.stackingInterfaces {
		remoteDP, remotePort, localInterface := d.mustGetStackingInterface(stackingInterface)

		ofport, _, err := d.addInternalPort(dpName, localInterface, 0)
		if err != nil {
			log.Debugf("Error attaching veth [ %s ] to bridge [ %s ]", localInterface, dpName)
			return err
		}
		log.Infof("Attached veth [ %s ] to bridge [ %s ] ofport %d", localInterface, dpName, ofport)
		stackingConfig += fmt.Sprintf("%s: {stack: ", remoteDP)
		if d.stackPriority1 == remoteDP {
			stackingConfig += "{priority: 1}, "
		}
		stackingConfig += fmt.Sprintf("interfaces: {%d: {description: %s, stack: {dp: %s, port: %d}}}}, ", remotePort, "Stack link to "+dpName, dpName, ofport)
		stackingPorts = append(stackingPorts, StackingPort{RemoteDP: remoteDP, RemotePort: remotePort, OFPort: ofport})
	}

	stackingConfig += fmt.Sprintf("%s: {dp_id: %d, description: %s, hardware: Open vSwitch, interfaces: {",
		dpName,
		intDpid,
		"Dovesnap Stacking Bridge for "+hostname)
	for _, stackingPort := range stackingPorts {
		stackingConfig += fmt.Sprintf("%d: {description: %s, stack: {dp: %s, port: %d}},",
			stackingPort.OFPort,
			"Stack link to "+stackingPort.RemoteDP,
			stackingPort.RemoteDP,
			stackingPort.RemotePort)
	}
	stackingConfig += "}}}}"

	setFaucetConfigFile(d.faucetclient, stackingConfig)
	return nil
}

func (d *Driver) CreateNetwork(r *networkplugin.CreateNetworkRequest) (err error) {
	log.Debugf("Create network request: %+v", r)
	err = nil

	defer func() {
		if rerr := recover(); rerr != nil {
			err = fmt.Errorf("Cannot create network: %v", rerr)
			if _, ok := d.networks[r.NetworkID]; ok {
				delete(d.networks, r.NetworkID)
			}
		}
	}()

	bridgeName := mustGetBridgeName(r)
	mtu := mustGetBridgeMTU(r)
	mode := mustGetBridgeMode(r)
	bindInterface := mustGetBindInterface(r)
	controller := mustGetBridgeController(r)
	if controller == "" {
		controller = d.stackDefaultControllers
	}
	dpid := mustGetBridgeDpid(r)
	vlan := mustGetBridgeVLAN(r)
	add_ports := mustGetBridgeAddPorts(r)
	gateway, mask := mustGetGatewayIP(r)

	ns := &NetworkState{
		BridgeName:        bridgeName,
		BridgeDpid:        dpid,
		BridgeVLAN:        vlan,
		MTU:               mtu,
		Mode:              mode,
		Gateway:           gateway,
		GatewayMask:       mask,
		FlatBindInterface: bindInterface,
	}
	d.networks[r.NetworkID] = ns
	d.stackMirrorConfigs[r.NetworkID] = d.getStackMirrorConfig(r)

	log.Debugf("Initializing bridge for network %s", r.NetworkID)

	if err := d.initBridge(r.NetworkID, controller, dpid, add_ports); err != nil {
		panic(err)
	}

	createmap := OFPortMap{
		OFPort:     0,
		AddPorts:   add_ports,
		Mode:       mode,
		NetworkID:  r.NetworkID,
		EndpointID: bridgeName,
		Operation:  "create",
	}

	d.ofportmapChan <- createmap
	return err
}

func (d *Driver) DeleteNetwork(r *networkplugin.DeleteNetworkRequest) error {
	log.Debugf("Delete network request: %+v", r)
	bridgeName := d.networks[r.NetworkID].BridgeName
	log.Debugf("Deleting Bridge %s", bridgeName)

	// remove the bridge from the faucet config if it exists
	networkName := d.networks[r.NetworkID].NetworkName
	log.Debugf("Deleting DP %s from Faucet", networkName)
	dp := []*faucetconfserver.DpInfo{
		{
			Name: networkName,
		},
	}
	dReq := &faucetconfserver.DelDpsRequest{
		InterfacesConfig: dp,
	}

	_, err := d.faucetclient.DelDps(context.Background(), dReq)
	if err != nil {
		log.Errorf("Error while calling DelDps %s: %v", dReq, err)
		return err
	}

	if usingMirrorBridge(d) {
		err := d.deletePatchPort(bridgeName, mirrorBridgeName)
		if err != nil {
			log.Errorf("Unable to delete patch port to mirror bridge because: %v", err)
		}
	}

	if usingStacking(d) {
		_, stackDpName, _ := d.getStackDP()
		err := d.deletePatchPort(bridgeName, stackDpName)
		if err != nil {
			log.Errorf("Unable to delete patch port between bridges because: %v", err)
		}
		if usingStackMirroring(d) {
			lbBridgeName := d.mustGetLoopbackDP()
			err = d.deletePatchPort(bridgeName, lbBridgeName)
			if err != nil {
				log.Errorf("Unable to delete patch port to loopback bridge: %v", err)
			}
		}
	}

	_, err = d.deleteBridge(bridgeName)
	if err != nil {
		log.Errorf("Deleting bridge %s failed: %s", bridgeName, err)
		return err
	}

	delete(d.networks, r.NetworkID)
	delete(d.stackMirrorConfigs, r.NetworkID)
	return nil
}

func (d *Driver) CreateEndpoint(r *networkplugin.CreateEndpointRequest) (*networkplugin.CreateEndpointResponse, error) {
	log.Debugf("Create endpoint request: %+v", r)
	localVethPair := vethPair(truncateID(r.EndpointID))
	log.Debugf("Create vethPair")
	res := &networkplugin.CreateEndpointResponse{Interface: &networkplugin.EndpointInterface{MacAddress: localVethPair.Attrs().HardwareAddr.String()}}
	log.Debugf("Attached veth5 %+v,", r.Interface)
	return res, nil
}

func (d *Driver) GetCapabilities() (*networkplugin.CapabilitiesResponse, error) {
	log.Debugf("Get capabilities request")
	res := &networkplugin.CapabilitiesResponse{
		Scope: "local",
	}
	return res, nil
}

func (d *Driver) ProgramExternalConnectivity(r *networkplugin.ProgramExternalConnectivityRequest) error {
	log.Debugf("Program external connectivity request: %+v", r)
	return nil
}

func (d *Driver) RevokeExternalConnectivity(r *networkplugin.RevokeExternalConnectivityRequest) error {
	log.Debugf("Revoke external connectivity request: %+v", r)
	return nil
}

func (d *Driver) FreeNetwork(r *networkplugin.FreeNetworkRequest) error {
	log.Debugf("Free network request: %+v", r)
	return nil
}

func (d *Driver) DiscoverNew(r *networkplugin.DiscoveryNotification) error {
	log.Debugf("Discover new request: %+v", r)
	return nil
}

func (d *Driver) DiscoverDelete(r *networkplugin.DiscoveryNotification) error {
	log.Debugf("Discover delete request: %+v", r)
	return nil
}

func (d *Driver) DeleteEndpoint(r *networkplugin.DeleteEndpointRequest) error {
	log.Debugf("Delete endpoint request: %+v", r)
	return nil
}

func (d *Driver) AllocateNetwork(r *networkplugin.AllocateNetworkRequest) (*networkplugin.AllocateNetworkResponse, error) {
	log.Debugf("Allocate network request: %+v", r)
	res := &networkplugin.AllocateNetworkResponse{
		Options: make(map[string]string),
	}
	return res, nil
}

func (d *Driver) EndpointInfo(r *networkplugin.InfoRequest) (*networkplugin.InfoResponse, error) {
	res := &networkplugin.InfoResponse{
		Value: make(map[string]string),
	}
	return res, nil
}

func (d *Driver) Join(r *networkplugin.JoinRequest) (*networkplugin.JoinResponse, error) {
	log.Debugf("Join request: %+v", r)
	localVethPair := vethPair(truncateID(r.EndpointID))
	if err := netlink.LinkAdd(localVethPair); err != nil {
		log.Errorf("failed to create the veth pair named: [ %v ] error: [ %s ]", localVethPair, err)
		return nil, err
	}
	// Bring the veth pair up
	err := netlink.LinkSetUp(localVethPair)
	if err != nil {
		log.Warnf("Error enabling veth local iface: [ %v ]", localVethPair)
		return nil, err
	}
	bridgeName := d.networks[r.NetworkID].BridgeName
	ofport, _, err := d.addInternalPort(bridgeName, localVethPair.Name, 0)
	if err != nil {
		log.Debugf("Error attaching veth [ %s ] to bridge [ %s ]", localVethPair.Name, bridgeName)
		return nil, err
	}
	log.Infof("Attached veth [ %s ] to bridge [ %s ] ofport %d", localVethPair.Name, bridgeName, ofport)

	// SrcName gets renamed to DstPrefix + ID on the container iface
	res := &networkplugin.JoinResponse{
		InterfaceName: networkplugin.InterfaceName{
			SrcName:   localVethPair.PeerName,
			DstPrefix: containerEthName,
		},
		Gateway: d.networks[r.NetworkID].Gateway,
	}
	log.Debugf("Join endpoint %s:%s to %s", r.NetworkID, r.EndpointID, r.SandboxKey)
	addmap := OFPortMap{
		OFPort:     ofport,
		AddPorts:   "",
		Mode:       "",
		NetworkID:  r.NetworkID,
		EndpointID: r.EndpointID,
		Operation:  "add",
	}
	d.ofportmapChan <- addmap
	return res, nil
}

func (d *Driver) Leave(r *networkplugin.LeaveRequest) error {
	log.Debugf("Leave request: %+v", r)
	portID := fmt.Sprintf(ovsPortPrefix + truncateID(r.EndpointID))
	bridgeName := d.networks[r.NetworkID].BridgeName
	ofport, err := d.ovsdber.getOfPortNumber(portID)
	if err != nil {
		log.Errorf("Unable to get ofport number from %s", portID)
		return err
	}
	localVethPair := vethPair(truncateID(r.EndpointID))
	if err := netlink.LinkDel(localVethPair); err != nil {
		log.Errorf("Unable to delete veth on leave: %s", err)
	}
	err = d.ovsdber.deletePort(bridgeName, portID)
	if err != nil {
		log.Errorf("OVS port [ %s ] delete transaction failed on bridge [ %s ] due to: %s", portID, bridgeName, err)
		return err
	}
	log.Infof("Deleted OVS port [ %s ] from bridge [ %s ]", portID, bridgeName)
	log.Debugf("Leave %s:%s", r.NetworkID, r.EndpointID)
	rmmap := OFPortMap{
		OFPort:     ofport,
		AddPorts:   "",
		Mode:       "",
		NetworkID:  r.NetworkID,
		EndpointID: r.EndpointID,
		Operation:  "rm",
	}
	d.ofportmapChan <- rmmap
	return nil
}

func getContainerFromEndpoint(dockerclient *client.Client, EndpointID string) (types.ContainerJSON, types.NetworkResource, error) {
	for i := 0; i < dockerRetries; i++ {
		netlist, _ := dockerclient.NetworkList(context.Background(), types.NetworkListOptions{})
		for _, net := range netlist {
			if net.Driver != DriverName {
				continue
			}
			netInspect, err := dockerclient.NetworkInspect(context.Background(), net.ID, types.NetworkInspectOptions{})
			if err != nil {
				continue
			}
			for containerID, containerInfo := range netInspect.Containers {
				if containerInfo.EndpointID == EndpointID {
					containerInspect, err := dockerclient.ContainerInspect(context.Background(), containerID)
					if err != nil {
						continue
					}
					return containerInspect, netInspect, nil
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
	return types.ContainerJSON{}, types.NetworkResource{}, fmt.Errorf("Endpoint %s not found", EndpointID)
}

func mustGetNetworkNameFromID(dockerclient *client.Client, NetworkID string) string {
	for i := 0; i < dockerRetries; i++ {
		netInspect, err := dockerclient.NetworkInspect(context.Background(), NetworkID, types.NetworkInspectOptions{})
		if err == nil {
			return netInspect.Name
		}
		time.Sleep(1 * time.Second)
	}
	panic(fmt.Errorf("Network %s not found", NetworkID))
}

func mustGetNetworkInspectFromID(dockerclient *client.Client, NetworkID string) types.NetworkResource {
	for i := 0; i < dockerRetries; i++ {
		netInspect, err := dockerclient.NetworkInspect(context.Background(), NetworkID, types.NetworkInspectOptions{})
		if err == nil {
			return netInspect
		}
		time.Sleep(1 * time.Second)
	}
	panic(fmt.Errorf("Network %s not found", NetworkID))
}

func mustGetIntDpid(dpid string) int {
	strDpid, _ := bc.Convert(strings.ToLower(dpid[2:]), bc.DigitsHex, bc.DigitsDec)
	intDpid, err := strconv.Atoi(strDpid)
	if err != nil {
		panic(fmt.Errorf("Unable convert dp_id to an int because: %v", err))
	}
	return intDpid
}

func mergeInterfacesYaml(dpName string, intDpid int, description string, addInterfaces string) string {
	return fmt.Sprintf("{dps: {%s: {dp_id: %d, description: OVS Bridge %s, interfaces: {%s}}}}",
		dpName, intDpid, description, addInterfaces)
}

func mustHandleCreate(d *Driver, confclient faucetconfserver.FaucetConfServerClient, mapMsg OFPortMap) {
	defer func() {
		if rerr := recover(); rerr != nil {
			log.Errorf("mustHandleCreate failed: %v", rerr)
		}
	}()

	log.Debugf("network ID: %s", mapMsg.NetworkID)
	netInspect := mustGetNetworkInspectFromID(d.dockerclient, mapMsg.NetworkID)
	d.networks[mapMsg.NetworkID].NetworkName = netInspect.Name
	bridgeName, dpid, vlan, err := getBridgeFromResource(&netInspect)
	if err != nil {
		panic(err)
	}
	intDpid := mustGetIntDpid(dpid)
	add_ports := mapMsg.AddPorts
	add_interfaces := ""
	if add_ports != "" {
		for _, add_port_number_str := range strings.Split(add_ports, ",") {
			add_port_number := strings.Split(add_port_number_str, "/")
			add_port := add_port_number[0]
			ofport, err := d.ovsdber.getOfPortNumber(add_port)
			if err != nil {
				panic(fmt.Errorf("Unable to get ofport number from %s", add_port))
			}
			add_interfaces += fmt.Sprintf("%d: {description: %s, native_vlan: %d},", ofport, "Physical interface "+add_port, vlan)
		}
	}
	mode := mapMsg.Mode
	if mode == "nat" {
		add_interfaces += fmt.Sprintf("%d: {description: OVS Port for NAT, native_vlan: %d},", ofPortLocal, vlan)
	}
	configYaml := mergeInterfacesYaml(netInspect.Name, intDpid, bridgeName, add_interfaces)
	if usingMirrorBridge(d) {
		stackMirrorConfig := d.stackMirrorConfigs[mapMsg.NetworkID]
		ofportNum, mirrorOfportNum, err := d.addPatchPort(bridgeName, mirrorBridgeName, uint(stackMirrorConfig.LbPort), 0)
		if err != nil {
			panic(err)
		}
		flowStr := fmt.Sprintf("priority=2,in_port=%d,actions=mod_vlan_vid:%d,output:1", mirrorOfportNum, vlan)
		mustOfCtl("add-flow", mirrorBridgeName, flowStr)
		add_interfaces += fmt.Sprintf("%d: {description: mirror, output_only: true},", ofportNum)
		configYaml = mergeInterfacesYaml(netInspect.Name, intDpid, bridgeName, add_interfaces)
	}
	if usingStacking(d) {
		_, stackDpName, err := d.getStackDP()
		if err != nil {
			panic(err)
		}
		ofportNum, ofportNumPeer, err := d.addPatchPort(bridgeName, stackDpName, 0, 0)
		if err != nil {
			panic(err)
		}
		localDpYaml := fmt.Sprintf("%s: {dp_id: %d, description: %s, interfaces: {%s %d: {description: %s, stack: {dp: %s, port: %d}}}}",
			netInspect.Name,
			intDpid,
			"OVS Bridge "+bridgeName,
			add_interfaces,
			ofportNum,
			"Stack link to "+stackDpName,
			stackDpName,
			ofportNumPeer)
		remoteDpYaml := fmt.Sprintf("%s: {interfaces: {%d: {description: %s, stack: {dp: %s, port: %d}}}}",
			stackDpName,
			ofportNumPeer,
			"Stack link to "+netInspect.Name,
			netInspect.Name,
			ofportNum)
		configYaml = fmt.Sprintf("{dps: {%s, %s}}", localDpYaml, remoteDpYaml)
	}
	setFaucetConfigFile(d.faucetclient, configYaml)
	if usingStackMirroring(d) {
		lbBridgeName := d.mustGetLoopbackDP()
		stackMirrorConfig := d.stackMirrorConfigs[mapMsg.NetworkID]
		_, _, err = d.addPatchPort(bridgeName, lbBridgeName, uint(stackMirrorConfig.LbPort), 0)
		if err != nil {
			panic(err)
		}
		req := &faucetconfserver.SetRemoteMirrorPortRequest{
			DpName:       netInspect.Name,
			PortNo:       stackMirrorConfig.LbPort,
			TunnelVid:    stackMirrorConfig.TunnelVid,
			RemoteDpName: stackMirrorConfig.RemoteDpName,
			RemotePortNo: stackMirrorConfig.RemoteMirrorPort,
		}
		_, err = d.faucetclient.SetRemoteMirrorPort(context.Background(), req)
		if err != nil {
			panic(err)
		}
	}
}

func mustHandleAdd(d *Driver, confclient faucetconfserver.FaucetConfServerClient, mapMsg OFPortMap, OFPorts *map[string]OFPortContainer) {
	defer func() {
		if rerr := recover(); rerr != nil {
			log.Errorf("mustHandleAdd failed: %v", rerr)
		}
	}()
	containerInspect, netInspect, err := getContainerFromEndpoint(d.dockerclient, mapMsg.EndpointID)
	if err != nil {
		panic(err)
	}
	bridgeName, dpid, vlan, err := getBridgeFromResource(&netInspect)
	if err != nil {
		panic(err)
	}
	intDpid := mustGetIntDpid(dpid)
	(*OFPorts)[mapMsg.EndpointID] = OFPortContainer{
		OFPort:           mapMsg.OFPort,
		containerInspect: containerInspect,
	}
	pid := containerInspect.State.Pid
	procPath := fmt.Sprintf("/proc/%d/ns/net", pid)
	log.Debugf(procPath)
	procNetNsPath := fmt.Sprintf("%s/%s", netNsPath, containerInspect.ID)
	log.Debugf(procNetNsPath)
	err = os.Symlink(procPath, procNetNsPath)
	if err != nil {
		panic(err)
	}
	log.Infof("Adding %s (pid %d) on %s DPID %d OFPort %d to Faucet",
		containerInspect.Name, pid, bridgeName, intDpid, mapMsg.OFPort)

	portacl := ""
	portacl, ok := containerInspect.Config.Labels["dovesnap.faucet.portacl"]
	if ok && len(portacl) > 0 {
		log.Infof("Set portacl %s on %s", portacl, containerInspect.Name)
	}
	add_interfaces := fmt.Sprintf("%d: {description: '%s', native_vlan: %d, acls_in: [%s]},",
		mapMsg.OFPort, fmt.Sprintf("%s %s", containerInspect.Name, truncateID(containerInspect.ID)), vlan, portacl)

	setFaucetConfigFile(d.faucetclient, mergeInterfacesYaml(netInspect.Name, intDpid, bridgeName, add_interfaces))

	mirror, ok := containerInspect.Config.Labels["dovesnap.faucet.mirror"]
	if ok && parseBool(mirror) {
		log.Infof("Mirroring container %s", containerInspect.Name)
		stackMirrorConfig := d.stackMirrorConfigs[mapMsg.NetworkID]
		if usingStackMirroring(d) || usingMirrorBridge(d) {
			req := &faucetconfserver.AddPortMirrorRequest{
				DpName:       netInspect.Name,
				PortNo:       uint32(mapMsg.OFPort),
				MirrorPortNo: stackMirrorConfig.LbPort,
			}
			_, err := confclient.AddPortMirror(context.Background(), req)
			if err != nil {
				panic(err)
			}
		}
	}
}

func mustHandleRm(d *Driver, confclient faucetconfserver.FaucetConfServerClient, mapMsg OFPortMap, OFPorts *map[string]OFPortContainer) {
	defer func() {
		if rerr := recover(); rerr != nil {
			log.Errorf("mustHandleRm failed: %v", rerr)
		}
	}()
	networkName := d.networks[mapMsg.NetworkID].NetworkName
	interfaces := &faucetconfserver.InterfaceInfo{
		PortNo: int32(mapMsg.OFPort),
	}

	log.Debugf("Removing port %d on %s from Faucet config", mapMsg.OFPort, networkName)

	// TODO: faucetconfrpc should clean up the mirror reference.
	if usingStackMirroring(d) || usingMirrorBridge(d) {
		stackMirrorConfig := d.stackMirrorConfigs[mapMsg.NetworkID]
		req := &faucetconfserver.RemovePortMirrorRequest{
			DpName:       networkName,
			PortNo:       uint32(mapMsg.OFPort),
			MirrorPortNo: stackMirrorConfig.LbPort,
		}
		// TODO: need a way to know if the container was started with mirroring label
		//       at this point the container is already removed, so can't inspect it
		_, err := confclient.RemovePortMirror(context.Background(), req)
		if err != nil {
			log.Errorf("Error unmirroring %v: %v", req, err)
		}
	}

	interfacesConf := []*faucetconfserver.DpInfo{
		{
			Name:       networkName,
			Interfaces: []*faucetconfserver.InterfaceInfo{interfaces},
		},
	}

	req := &faucetconfserver.DelDpInterfacesRequest{
		InterfacesConfig: interfacesConf,
		DeleteEmptyDp:    true,
	}
	_, err := confclient.DelDpInterfaces(context.Background(), req)
	if err != nil {
		log.Errorf("Error while calling DelDpInterfaces RPC %s: %v", req, err)
	}

	// The container will be gone by the time we query docker.
	delete(*OFPorts, mapMsg.EndpointID)
}

func consolidateDockerInfo(d *Driver, confclient faucetconfserver.FaucetConfServerClient) {
	OFPorts := make(map[string]OFPortContainer)

	for {
		mapMsg := <-d.ofportmapChan
		switch mapMsg.Operation {
		case "create":
			mustHandleCreate(d, confclient, mapMsg)
		case "add":
			mustHandleAdd(d, confclient, mapMsg, &OFPorts)
		case "rm":
			mustHandleRm(d, confclient, mapMsg, &OFPorts)
		default:
			log.Errorf("Unknown consolidation message: %s", mapMsg)
		}
	}
}

func mustGetGRPCClient(flagFaucetconfrpcServerName string, flagFaucetconfrpcServerPort int, flagFaucetconfrpcKeydir string) faucetconfserver.FaucetConfServerClient {
	crt_file := flagFaucetconfrpcKeydir + "/faucetconfrpc.crt"
	key_file := flagFaucetconfrpcKeydir + "/faucetconfrpc.key"
	ca_file := flagFaucetconfrpcKeydir + "/" + flagFaucetconfrpcServerName + "-ca.crt"
	certificate, err := tls.LoadX509KeyPair(crt_file, key_file)
	if err != nil {
		panic(err)
	}
	log.Debugf("Certificates loaded")
	certPool := x509.NewCertPool()
	ca, err := ioutil.ReadFile(ca_file)
	if err != nil {
		panic(err)
	}
	if err := certPool.AppendCertsFromPEM(ca); !err {
		panic(err)
	}
	creds := credentials.NewTLS(&tls.Config{
		ServerName:   flagFaucetconfrpcServerName,
		Certificates: []tls.Certificate{certificate},
		RootCAs:      certPool,
	})

	// Connect to faucetconfrpc server.
	addr := flagFaucetconfrpcServerName + ":" + strconv.Itoa(flagFaucetconfrpcServerPort)
	log.Debugf("Connecting to RPC server: %v", addr)
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(creds), grpc.WithBlock(), grpc.WithTimeout(30*time.Second))
	if err != nil {
		panic(err)
	}
	log.Debugf("Connected to RPC server")
	confclient := faucetconfserver.NewFaucetConfServerClient(conn)
	_, err = confclient.GetConfigFile(context.Background(), &faucetconfserver.GetConfigFileRequest{})
	if err != nil {
		panic(err)
	}
	log.Debugf("Successfully retrieved Faucet config")
	return confclient
}

func usingMirrorBridge(d *Driver) bool {
	return len(d.mirrorBridgeOut) != 0
}

func usingStacking(d *Driver) bool {
	return !usingMirrorBridge(d) && len(d.stackingInterfaces[0]) != 0
}

func usingStackMirroring(d *Driver) bool {
	return usingStacking(d) && len(d.stackMirrorInterface) > 1
}

func waitForOvs(d *Driver) {
	for i := 0; i < ovsStartupRetries; i++ {
		_, err := d.ovsdber.show()
		if err == nil {
			break
		}
		log.Infof("Waiting for open vswitch")
		time.Sleep(5 * time.Second)
	}
	_, err := d.ovsdber.show()
	if err != nil {
		panic(fmt.Errorf("Could not connect to open vswitch"))
	}
	log.Infof("Connected to open vswitch")
}

func restoreNetworks(d *Driver) {
	netlist, err := d.dockerclient.NetworkList(context.Background(), types.NetworkListOptions{})
	if err != nil {
		panic(fmt.Errorf("Could not get docker networks: %s", err))
	}
	for _, net := range netlist {
		if net.Driver == DriverName {
			netInspect, err := d.dockerclient.NetworkInspect(context.Background(), net.ID, types.NetworkInspectOptions{})
			if err != nil {
				panic(fmt.Errorf("Could not inspect docker networks inpect: %s", err))
			}
			bridgeName, dpid, vlan, err := getBridgeFromResource(&netInspect)
			if err != nil {
				continue
			}
			ns := &NetworkState{
				NetworkName: netInspect.Name,
				BridgeName:  bridgeName,
				BridgeDpid:  dpid,
				BridgeVLAN:  vlan,
			}
			d.networks[net.ID] = ns
			log.Debugf("Existing networks created by this driver: %v", netInspect.Name)
		}
	}
}

func createNetNsDir() {
	_, err := os.Stat(netNsPath)
	if os.IsNotExist(err) {
		err = os.MkdirAll(netNsPath, 0755)
		if err != nil {
			panic(err)
		}
	}
}

func NewDriver(flagFaucetconfrpcServerName string, flagFaucetconfrpcServerPort int, flagFaucetconfrpcKeydir string, flagStackPriority1 string, flagStackingInterfaces string, flagStackMirrorInterface string, flagDefaultControllers string, flagMirrorBridgeIn string, flagMirrorBridgeOut string) *Driver {
	createNetNsDir()

	stack_mirror_interface := strings.Split(flagStackMirrorInterface, ":")
	if len(flagStackMirrorInterface) > 0 && len(stack_mirror_interface) != 2 {
		panic(fmt.Errorf("Invalid stack mirror interface config: %s", flagStackMirrorInterface))
	}
	stacking_interfaces := strings.Split(flagStackingInterfaces, ",")
	log.Debugf("Stacking interfaces: %v", stacking_interfaces)
	confclient := mustGetGRPCClient(flagFaucetconfrpcServerName, flagFaucetconfrpcServerPort, flagFaucetconfrpcKeydir)

	docker, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		panic(fmt.Errorf("Could not connect to docker: %s", err))
	}

	d := &Driver{
		dockerclient:            docker,
		ovsdber:                 ovsdber{},
		faucetclient:            confclient,
		stackPriority1:          flagStackPriority1,
		stackingInterfaces:      stacking_interfaces,
		stackMirrorInterface:    stack_mirror_interface,
		stackDefaultControllers: flagDefaultControllers,
		mirrorBridgeIn:          flagMirrorBridgeIn,
		mirrorBridgeOut:         flagMirrorBridgeOut,
		networks:                make(map[string]*NetworkState),
		ofportmapChan:           make(chan OFPortMap, 2),
		stackMirrorConfigs:      make(map[string]StackMirrorConfig),
	}

	waitForOvs(d)

	if usingMirrorBridge(d) {
		d.createMirrorBridge()
	}

	if usingStacking(d) {
		stackerr := d.createStackingBridge()
		if stackerr != nil {
			panic(stackerr)
		}
		if usingStackMirroring(d) {
			lberr := d.createLoopbackBridge()
			if lberr != nil {
				panic(lberr)
			}
		}
	} else {
		log.Warnf("No stacking interface defined, not stacking DPs or creating a stacking bridge")
	}

	restoreNetworks(d)

	go consolidateDockerInfo(d, confclient)

	return d
}

// Create veth pair. Peername is renamed to eth0 in the container
func vethPair(suffix string) *netlink.Veth {
	return &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: ovsPortPrefix + suffix},
		PeerName:  peerOvsPortPrefix + suffix,
	}
}

// Enable a netlink interface
func interfaceUp(name string) error {
	iface, err := netlink.LinkByName(name)
	if err != nil {
		log.Debugf("Error retrieving a link named [ %s ]", iface.Attrs().Name)
		return err
	}
	return netlink.LinkSetUp(iface)
}

func truncateID(id string) string {
	return id[:5]
}

func getGenericIntOption(r *networkplugin.CreateNetworkRequest, optionName string, defaultOption int) int {
	option := getGenericOption(r, optionName)
	if option != "" {
		optionInt, err := strconv.Atoi(option)
		if err == nil {
			return optionInt
		}
	}
	return defaultOption
}

func mustGetTunnelVid(r *networkplugin.CreateNetworkRequest) int {
	return getGenericIntOption(r, mirrorTunnelVid, mustGetBridgeVLAN(r)+defaultTunnelVLANOffset)
}

func mustGetBridgeMTU(r *networkplugin.CreateNetworkRequest) int {
	return getGenericIntOption(r, mtuOption, defaultMTU)
}

func mustGetLbPort(r *networkplugin.CreateNetworkRequest) int {
	return getGenericIntOption(r, bridgeLbPort, defaultLbPort)
}

func mustGetBridgeName(r *networkplugin.CreateNetworkRequest) string {
	bridgeName := bridgePrefix + truncateID(r.NetworkID)
	name := getGenericOption(r, bridgeNameOption)
	if name != "" {
		bridgeName = name
	}
	return bridgeName
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

func mustGetBridgeVLAN(r *networkplugin.CreateNetworkRequest) int {
	return getGenericIntOption(r, vlanOption, defaultVLAN)
}

func mustGetBridgeAddPorts(r *networkplugin.CreateNetworkRequest) string {
	return getGenericOption(r, bridgeAddPorts)
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
	if r.Options != nil {
		if mode, ok := r.Options[bindInterfaceOption].(string); ok {
			return mode
		}
	}
	// As bind interface is optional and has no default, don't return an error
	return ""
}

func mustGetBridgeNameFromResource(r *types.NetworkResource) string {
	return bridgePrefix + truncateID(r.ID)
}

func mustGetBridgeDpidFromResource(r *types.NetworkResource) string {
	if r.Options != nil {
		if dpid, ok := r.Options[bridgeDpid]; ok {
			return dpid
		}
	}
	panic("No DPID found for this network")
}

func mustGetBridgeVlanFromResource(r *types.NetworkResource) int {
	if r.Options != nil {
		vlan, err := strconv.Atoi(r.Options[vlanOption])
		if err == nil {
			return vlan
		}
	}
	log.Infof("No VLAN found for this network, using default: %d", defaultVLAN)
	return defaultVLAN
}

func getBridgeFromResource(r *types.NetworkResource) (bridgeName string, dpid string, vlan int, err error) {
	defer func() {
		err = nil
		if rerr := recover(); rerr != nil {
			err = fmt.Errorf("missing bridge info: %v", rerr)
			bridgeName = ""
			dpid = ""
			vlan = 0
		}
	}()
	bridgeName = mustGetBridgeNameFromResource(r)
	dpid = mustGetBridgeDpidFromResource(r)
	vlan = mustGetBridgeVlanFromResource(r)
	return
}

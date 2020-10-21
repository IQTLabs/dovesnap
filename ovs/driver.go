package ovs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	networkplugin "github.com/docker/go-plugins-helpers/network"
	log "github.com/sirupsen/logrus"
)

type ContainerState struct {
	Name       string
	Id         string
	OFPort     uint32
	MacAddress string
	HostIP     string
	Labels     map[string]string
}

type ExternalPortState struct {
	Name   string
	OFPort uint32
}

type NetworkState struct {
	NetworkName       string
	BridgeName        string
	BridgeDpid        string
	BridgeDpidUint    uint64
	BridgeVLAN        uint
	MTU               uint
	Mode              string
	AddPorts          string
	Gateway           string
	GatewayMask       string
	FlatBindInterface string
	UseDHCP           bool
	Userspace         bool
	NATAcl            string
	OvsLocalMac       string
	Containers        map[string]ContainerState
	ExternalPorts     map[string]ExternalPortState
}

type DovesnapOp struct {
	NewNetworkState      NetworkState
	NewStackMirrorConfig StackMirrorConfig
	AddPorts             string
	Mode                 string
	NetworkID            string
	EndpointID           string
	Options              map[string]interface{}
	Operation            string
}

type NotifyMsg struct {
	NetworkState NetworkState
	Type         string
	Operation    string
	Details      map[string]string
}

type NotifyMsgJson struct {
	Version uint
	Time    int64
	Msg     NotifyMsg
}

type StackingPort struct {
	OFPort     uint32
	RemoteDP   string
	RemotePort uint32
}

type OFPortContainer struct {
	OFPort           uint32
	containerInspect types.ContainerJSON
	udhcpcCmd        *exec.Cmd
	Options          map[string]interface{}
}

type Driver struct {
	dockerer
	faucetconfrpcer
	ovsdber
	stackPriority1          string
	stackingInterfaces      []string
	stackMirrorInterface    []string
	stackDefaultControllers string
	mirrorBridgeIn          string
	mirrorBridgeOut         string
	networks                map[string]NetworkState
	dovesnapOpChan          chan DovesnapOp
	notifyMsgChan           chan NotifyMsg
	webResponseChan         chan string
	stackMirrorConfigs      map[string]StackMirrorConfig
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
	err = d.ovsdber.createBridge(mirrorBridgeName, "", "", add_ports, true, false, "")
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

	err = d.ovsdber.createBridge(dpName, d.stackDefaultControllers, dpid, "", true, false, "")
	if err != nil {
		log.Errorf("Unable to create stacking bridge because: [ %s ]", err)
	}

	// loop through stacking interfaces
	stackingPorts := []StackingPort{}
	stackingConfig := "{dps: {"
	for _, stackingInterface := range d.stackingInterfaces {
		remoteDP, remotePort, localInterface := d.mustGetStackingInterface(stackingInterface)

		ofport, _ := d.mustAddInternalPort(dpName, localInterface, 0)
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

	d.faucetconfrpcer.mustSetFaucetConfigFile(stackingConfig)
	return nil
}

func (d *Driver) CreateNetwork(r *networkplugin.CreateNetworkRequest) (err error) {
	log.Debugf("Create network request: %+v", r)
	return d.ReOrCreateNetwork(r, "create")
}

func (d *Driver) ReOrCreateNetwork(r *networkplugin.CreateNetworkRequest, operation string) (err error) {
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
	useDHCP := mustGetUseDHCP(r)
	useUserspace := mustGetUserspace(r)
	natAcl := mustGetNATAcl(r)
	ovsLocalMac := mustGetOvsLocalMac(r)

	if useDHCP {
		if mode != "flat" {
			panic(fmt.Errorf("network must be flat when DHCP in use"))
		}
		if gateway != "" {
			panic(fmt.Errorf("network must not have IP config when DHCP in use"))
		}
		if !mustGetInternalOption(r) {
			panic(fmt.Errorf("network must be internal when DHCP in use"))
		}
	}

	ns := NetworkState{
		BridgeName:        bridgeName,
		BridgeDpid:        dpid,
		BridgeDpidUint:    mustGetUintFromHexStr(dpid),
		BridgeVLAN:        vlan,
		MTU:               mtu,
		Mode:              mode,
		AddPorts:          add_ports,
		Gateway:           gateway,
		GatewayMask:       mask,
		FlatBindInterface: bindInterface,
		UseDHCP:           useDHCP,
		Userspace:         useUserspace,
		NATAcl:            natAcl,
		OvsLocalMac:       ovsLocalMac,
		Containers:        make(map[string]ContainerState),
		ExternalPorts:     make(map[string]ExternalPortState),
	}

	if operation == "create" {
		if err := d.initBridge(ns, controller, dpid, add_ports, useUserspace, ovsLocalMac); err != nil {
			panic(err)
		}
	}

	createMsg := DovesnapOp{
		NewNetworkState:      ns,
		NewStackMirrorConfig: d.getStackMirrorConfig(r),
		AddPorts:             add_ports,
		Mode:                 mode,
		NetworkID:            r.NetworkID,
		EndpointID:           bridgeName,
		Operation:            operation,
	}

	d.dovesnapOpChan <- createMsg
	return err
}

func (d *Driver) DeleteNetwork(r *networkplugin.DeleteNetworkRequest) error {
	log.Debugf("Delete network request: %+v", r)
	deleteMsg := DovesnapOp{
		NetworkID: r.NetworkID,
		Operation: "delete",
	}

	d.dovesnapOpChan <- deleteMsg
	return nil
}

func (d *Driver) CreateEndpoint(r *networkplugin.CreateEndpointRequest) (*networkplugin.CreateEndpointResponse, error) {
	log.Debugf("Create endpoint request: %+v", r)
	macAddress := r.Interface.MacAddress
	localVethPair := vethPair(truncateID(r.EndpointID))
	addVethPair(localVethPair)
	vethName := localVethPair.PeerName
	if macAddress == "" {
		// No MAC address requested, we provide our own.
		macAddress = getMacAddr(vethName)
	} else {
		mustSetInterfaceMac(vethName, macAddress)
		// We accept Docker's request.
		macAddress = ""
	}
	res := &networkplugin.CreateEndpointResponse{Interface: &networkplugin.EndpointInterface{MacAddress: macAddress}}
	log.Debugf("Create endpoint response: %+v", res.Interface)
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
	ns := d.networks[r.NetworkID]
	res := &networkplugin.JoinResponse{
		InterfaceName: networkplugin.InterfaceName{
			// SrcName gets renamed to DstPrefix + ID on the container iface
			SrcName:   localVethPair.PeerName,
			DstPrefix: containerEthName,
		},
		Gateway: ns.Gateway,
	}
	log.Debugf("Join endpoint %s:%s to %s", r.NetworkID, r.EndpointID, r.SandboxKey)
	joinMsg := DovesnapOp{
		NetworkID:  r.NetworkID,
		EndpointID: r.EndpointID,
		Options:    r.Options,
		Operation:  "join",
	}
	d.dovesnapOpChan <- joinMsg
	return res, nil
}

func (d *Driver) Leave(r *networkplugin.LeaveRequest) error {
	log.Debugf("Leave request: %+v", r)
	leaveMsg := DovesnapOp{
		NetworkID:  r.NetworkID,
		EndpointID: r.EndpointID,
		Operation:  "leave",
	}
	d.dovesnapOpChan <- leaveMsg
	return nil
}

func mustHandleDeleteNetwork(d *Driver, opMsg DovesnapOp) {
	defer func() {
		if rerr := recover(); rerr != nil {
			log.Errorf("mustHandleDeleteNetwork failed: %v", rerr)
		}
	}()

	// remove the bridge from the faucet config if it exists
	ns := d.networks[opMsg.NetworkID]
	log.Infof("Deleting network ID %s bridge %s", opMsg.NetworkID, ns.BridgeName)

	d.faucetconfrpcer.mustDeleteDp(ns.NetworkName)

	if usingMirrorBridge(d) {
		d.mustDeletePatchPort(ns.BridgeName, mirrorBridgeName)
	}

	if usingStacking(d) {
		_, stackDpName, _ := d.getStackDP()
		d.mustDeletePatchPort(ns.BridgeName, stackDpName)
		if usingStackMirroring(d) {
			lbBridgeName := d.mustGetLoopbackDP()
			d.mustDeletePatchPort(ns.BridgeName, lbBridgeName)
		}
	}

	d.mustDeleteBridge(ns.BridgeName)

	delete(d.networks, opMsg.NetworkID)
	delete(d.stackMirrorConfigs, opMsg.NetworkID)

	d.notifyMsgChan <- NotifyMsg{
		Type:         "NETWORK",
		Operation:    "DELETE",
		NetworkState: ns,
	}
}

func mustHandleCreateNetwork(d *Driver, opMsg DovesnapOp) {
	defer func() {
		if rerr := recover(); rerr != nil {
			log.Errorf("mustHandleCreateNetwork failed: %v", rerr)
		}
	}()

	log.Debugf("network ID: %s", opMsg.NetworkID)
	netInspect := d.dockerer.mustGetNetworkInspectFromID(opMsg.NetworkID)
	inspectNs, err := getNetworkStateFromResource(&netInspect)
	if err != nil {
		panic(err)
	}

	ns := opMsg.NewNetworkState
	d.stackMirrorConfigs[opMsg.NetworkID] = opMsg.NewStackMirrorConfig
	ns.NetworkName = inspectNs.NetworkName
	d.networks[opMsg.NetworkID] = ns

	add_ports := opMsg.AddPorts
	add_interfaces := ""
	if add_ports != "" {
		for _, add_port_number_str := range strings.Split(add_ports, ",") {
			add_port_number := strings.Split(add_port_number_str, "/")
			add_port := add_port_number[0]
			ofport := d.ovsdber.mustGetOfPortNumber(add_port)
			add_interfaces += d.faucetconfrpcer.vlanInterfaceYaml(ofport, "Physical interface "+add_port, ns.BridgeVLAN, "")
			ns.ExternalPorts[add_port] = ExternalPortState{Name: add_port, OFPort: ofport}
		}
	}
	mode := opMsg.Mode
	if mode == "nat" {
		add_interfaces += d.faucetconfrpcer.vlanInterfaceYaml(ofPortLocal, "OVS Port for NAT", ns.BridgeVLAN, ns.NATAcl)
		ns.ExternalPorts[inspectNs.BridgeName] = ExternalPortState{Name: inspectNs.BridgeName, OFPort: ofPortLocal}
	}
	configYaml := d.faucetconfrpcer.mergeInterfacesYaml(ns.NetworkName, ns.BridgeDpidUint, ns.BridgeName, add_interfaces)
	if usingMirrorBridge(d) {
		log.Debugf("configuring mirror bridge port for %s", ns.BridgeName)
		stackMirrorConfig := d.stackMirrorConfigs[opMsg.NetworkID]
		ofportNum, mirrorOfportNum := d.mustAddPatchPort(ns.BridgeName, mirrorBridgeName, stackMirrorConfig.LbPort, 0)
		flowStr := fmt.Sprintf("priority=2,in_port=%d,dl_vlan=0xffff,actions=mod_vlan_vid:%d,output:1", mirrorOfportNum, ns.BridgeVLAN)
		mustOfCtl("add-flow", mirrorBridgeName, flowStr)
		add_interfaces += fmt.Sprintf("%d: {description: mirror, output_only: true},", ofportNum)
		ns.ExternalPorts[mirrorBridgeName] = ExternalPortState{Name: mirrorBridgeName, OFPort: ofportNum}
		configYaml = d.faucetconfrpcer.mergeInterfacesYaml(ns.NetworkName, ns.BridgeDpidUint, ns.BridgeName, add_interfaces)
	}
	if usingStacking(d) {
		_, stackDpName, err := d.getStackDP()
		if err != nil {
			panic(err)
		}
		ofportNum, ofportNumPeer := d.mustAddPatchPort(ns.BridgeName, stackDpName, 0, 0)
		ns.ExternalPorts[stackDpName] = ExternalPortState{Name: stackDpName, OFPort: ofportNum}
		localDpYaml := fmt.Sprintf("%s: {dp_id: %d, description: %s, interfaces: {%s %s}}",
			ns.NetworkName,
			ns.BridgeDpidUint,
			"OVS Bridge "+ns.BridgeName,
			add_interfaces,
			d.faucetconfrpcer.stackInterfaceYaml(ofportNum, stackDpName, ofportNumPeer))
		remoteDpYaml := fmt.Sprintf("%s: {interfaces: {%s}}",
			stackDpName,
			d.faucetconfrpcer.stackInterfaceYaml(ofportNumPeer, ns.NetworkName, ofportNum))
		configYaml = fmt.Sprintf("{dps: {%s, %s}}", localDpYaml, remoteDpYaml)
	}
	d.faucetconfrpcer.mustSetFaucetConfigFile(configYaml)
	if usingStackMirroring(d) {
		lbBridgeName := d.mustGetLoopbackDP()
		stackMirrorConfig := d.stackMirrorConfigs[opMsg.NetworkID]
		d.mustAddPatchPort(ns.BridgeName, lbBridgeName, stackMirrorConfig.LbPort, 0)
		d.faucetconfrpcer.mustSetRemoteMirrorPort(
			ns.NetworkName,
			stackMirrorConfig.LbPort,
			stackMirrorConfig.TunnelVid,
			stackMirrorConfig.RemoteDpName,
			stackMirrorConfig.RemoteMirrorPort,
		)
	}
	d.notifyMsgChan <- NotifyMsg{
		Type:         "NETWORK",
		Operation:    "CREATE",
		NetworkState: ns,
	}
}

func mustGetPortMap(portMapRaw interface{}) (string, string, string) {
	portMap := portMapRaw.(map[string]interface{})
	hostPort := fmt.Sprintf("%d", int(portMap["HostPort"].(float64)))
	port := fmt.Sprintf("%d", int(portMap["Port"].(float64)))
	ipProto := "tcp"
	ipProtoNum := int(portMap["Proto"].(float64))
	if ipProtoNum == 17 {
		ipProto = "udp"
	}
	return hostPort, port, ipProto
}

func mustHandleJoinContainer(d *Driver, opMsg DovesnapOp, OFPorts *map[string]OFPortContainer) {
	defer func() {
		if rerr := recover(); rerr != nil {
			log.Errorf("mustHandleJoinContainer failed: %v", rerr)
		}
	}()
	containerInspect, err := d.dockerer.getContainerFromEndpoint(opMsg.EndpointID)
	if err != nil {
		panic(err)
	}
	ns := d.networks[opMsg.NetworkID]
	pid := containerInspect.State.Pid
	containerNetSettings := containerInspect.NetworkSettings.Networks[ns.NetworkName]
	localVethPair := vethPair(truncateID(opMsg.EndpointID))
	vethName := localVethPair.Name
	macAddress := containerNetSettings.MacAddress
	ofport, _ := d.mustAddInternalPort(ns.BridgeName, vethName, 0)

	createNsLink(pid, containerInspect.ID)
	defaultInterface := "eth0"

	macPrefix, mok := containerInspect.Config.Labels["dovesnap.faucet.mac_prefix"]
	if mok && len(macPrefix) > 0 {
		oldMacAddress := macAddress
		macAddress := mustPrefixMAC(macPrefix, macAddress)
		log.Infof("mapping MAC from %s to %s using prefix %s", oldMacAddress, macAddress, macPrefix)
		output, err := exec.Command("ip", "netns", "exec", containerInspect.ID, "ip", "link", "set", defaultInterface, "address", macAddress).CombinedOutput()
		log.Debugf("%s", output)
		if err != nil {
			panic(err)
		}
	}

	log.Infof("Adding %s (pid %d) veth %s MAC %s on %s DPID %d OFPort %d to Faucet",
		containerInspect.Name, pid, vethName, macAddress, ns.BridgeName, ns.BridgeDpidUint, ofport)
	log.Debugf("container network settings: %+v", containerNetSettings)

	log.Debugf("%+v", opMsg.Options[portMapOption])
	hostIP := containerNetSettings.IPAddress
	gatewayIP := containerNetSettings.Gateway

	// Regular docker uses docker proxy, to listen on the configured port and proxy them into the container.
	// dovesnap doesn't get to use docker proxy, so we listen on the configured port on the network's gateway instead.
	for _, portMapRaw := range opMsg.Options[portMapOption].([]interface{}) {
		log.Debugf("adding portmap %+v", portMapRaw)
		hostPort, port, ipProto := mustGetPortMap(portMapRaw)
		mustAddGatewayPortMap(ns.BridgeName, ipProto, gatewayIP, hostIP, hostPort, port)
	}

	portacl := ""
	portacl, ok := containerInspect.Config.Labels["dovesnap.faucet.portacl"]
	if ok && len(portacl) > 0 {
		log.Infof("Set portacl %s on %s", portacl, containerInspect.Name)
	}
	add_interfaces := d.faucetconfrpcer.vlanInterfaceYaml(
		ofport, fmt.Sprintf("%s %s", containerInspect.Name, truncateID(containerInspect.ID)), ns.BridgeVLAN, portacl)

	d.faucetconfrpcer.mustSetFaucetConfigFile(d.faucetconfrpcer.mergeInterfacesYaml(ns.NetworkName, ns.BridgeDpidUint, ns.BridgeName, add_interfaces))

	mirror, ok := containerInspect.Config.Labels["dovesnap.faucet.mirror"]
	if ok && parseBool(mirror) {
		log.Infof("Mirroring container %s", containerInspect.Name)
		stackMirrorConfig := d.stackMirrorConfigs[opMsg.NetworkID]
		if usingStackMirroring(d) || usingMirrorBridge(d) {
			d.faucetconfrpcer.mustAddPortMirror(ns.NetworkName, ofport, stackMirrorConfig.LbPort)
		}
	}

	udhcpcCmd := exec.Command("ip", "netns", "exec", containerInspect.ID, "/sbin/udhcpc", "-f", "-R", "-i", defaultInterface)
	// TODO: If DHCP in use, need background process to obtain IP address.
	if ns.UseDHCP {
		err = udhcpcCmd.Start()
		if err != nil {
			panic(err)
		}
		log.Infof("started udhcpc for %s", containerInspect.ID)
	} else {
		udhcpcCmd = nil
	}
	if ns.Userspace {
		output, err := exec.Command("ip", "netns", "exec", containerInspect.ID, "/sbin/ethtool", "-K", defaultInterface, "tx", "off").CombinedOutput()
		log.Debugf("%s", output)
		if err != nil {
			panic(err)
		}
	}
	containerMap := OFPortContainer{
		OFPort:           ofport,
		containerInspect: containerInspect,
		udhcpcCmd:        udhcpcCmd,
		Options:          opMsg.Options,
	}
	(*OFPorts)[opMsg.EndpointID] = containerMap
	ns.Containers[opMsg.EndpointID] = ContainerState{
		Name:       containerInspect.Name,
		Id:         containerInspect.ID,
		OFPort:     ofport,
		HostIP:     hostIP,
		MacAddress: macAddress,
		Labels:     containerInspect.Config.Labels,
	}

	d.notifyMsgChan <- NotifyMsg{
		Type:         "CONTAINER",
		Operation:    "JOIN",
		NetworkState: ns,
		Details: map[string]string{
			"name": containerInspect.Name,
			"id":   containerInspect.ID,
			"port": fmt.Sprintf("%d", ofport),
			"mac":  macAddress,
			"ip":   hostIP,
		},
	}
}

func mustHandleLeaveContainer(d *Driver, opMsg DovesnapOp, OFPorts *map[string]OFPortContainer) {
	defer func() {
		if rerr := recover(); rerr != nil {
			log.Errorf("mustHandleLeaveContainer failed: %v", rerr)
		}
	}()
	containerMap := (*OFPorts)[opMsg.EndpointID]
	udhcpcCmd := containerMap.udhcpcCmd
	if udhcpcCmd != nil {
		log.Infof("Shutting down udhcpc")
		udhcpcCmd.Process.Kill()
		udhcpcCmd.Wait()
	}
	portID := fmt.Sprintf(ovsPortPrefix + truncateID(opMsg.EndpointID))
	ofport := d.ovsdber.mustGetOfPortNumber(portID)
	localVethPair := vethPair(truncateID(opMsg.EndpointID))
	delVethPair(localVethPair)

	ns := d.networks[opMsg.NetworkID]
	d.ovsdber.mustDeletePort(ns.BridgeName, portID)
	d.faucetconfrpcer.mustDeleteDpInterface(ns.NetworkName, ofport)

	containerNetSettings := containerMap.containerInspect.NetworkSettings.Networks[ns.NetworkName]
	hostIP := containerNetSettings.IPAddress
	gatewayIP := containerNetSettings.Gateway
	for _, portMapRaw := range containerMap.Options[portMapOption].([]interface{}) {
		hostPort, port, ipProto := mustGetPortMap(portMapRaw)
		mustDeleteGatewayPortMap(ns.BridgeName, ipProto, gatewayIP, hostIP, hostPort, port)
	}

	delete(*OFPorts, opMsg.EndpointID)
	delete(ns.Containers, opMsg.EndpointID)

	d.notifyMsgChan <- NotifyMsg{
		Type:         "CONTAINER",
		Operation:    "LEAVE",
		NetworkState: ns,
		Details: map[string]string{
			"name": containerMap.containerInspect.Name,
			"id":   containerMap.containerInspect.ID,
			"port": fmt.Sprintf("%d", ofport),
		},
	}
}

func reconcileOvs(d *Driver, allPortDesc *map[string]map[uint32]string) {
	for id, ns := range d.networks {
		stackMirrorConfig := d.stackMirrorConfigs[id]
		newPortDesc := make(map[uint32]string)
		mustScrapePortDesc(ns.BridgeName, &newPortDesc)
		addPorts := make(map[string]uint32)
		d.ovsdber.parseAddPorts(ns.AddPorts, &addPorts)

		portDesc, have_port_desc := (*allPortDesc)[id]
		if have_port_desc {
			if reflect.DeepEqual(newPortDesc, portDesc) {
				continue
			}
			log.Debugf("portDesc for %s updated", ns.BridgeName)

			for ofport, desc := range portDesc {
				_, have_new_port_desc := newPortDesc[ofport]
				if have_new_port_desc {
					continue
				}
				// Ignore container ports
				if strings.HasPrefix(desc, ovsPortPrefix) {
					continue
				}
				log.Infof("removing non dovesnap port: %s %s %d %s", id, ns.BridgeName, ofport, desc)
				d.faucetconfrpcer.mustDeleteDpInterface(ns.NetworkName, uint32(ofport))
				delete(ns.ExternalPorts, desc)
			}
		} else {
			log.Debugf("new portDesc for %s", ns.BridgeName)
		}

		add_interfaces := ""

		for ofport, desc := range newPortDesc {
			// Ignore NAT and mirror port
			if ofport == ofPortLocal || ofport == stackMirrorConfig.LbPort {
				continue
			}
			// Ignore container and patch ports.
			if strings.HasPrefix(desc, ovsPortPrefix) || strings.HasPrefix(desc, patchPrefix) {
				continue
			}
			// Skip ports that were added at creation time.
			_, have_add_port := addPorts[desc]
			if have_add_port {
				continue
			}
			log.Infof("adding non dovesnap port: %s %s %d %s", id, ns.BridgeName, ofport, desc)
			add_interfaces += d.faucetconfrpcer.vlanInterfaceYaml(ofport, "Physical interface "+desc, ns.BridgeVLAN, "")
			ns.ExternalPorts[desc] = ExternalPortState{Name: desc, OFPort: ofport}
		}

		if add_interfaces != "" {
			configYaml := d.faucetconfrpcer.mergeInterfacesYaml(ns.NetworkName, ns.BridgeDpidUint, ns.BridgeName, add_interfaces)
			d.faucetconfrpcer.mustSetFaucetConfigFile(configYaml)
		}

		(*allPortDesc)[id] = newPortDesc
	}
}

func mustHandleNetworks(d *Driver) {
	encodedMsg, err := json.Marshal(d.networks)
	if err != nil {
		panic(err)
	}
	d.webResponseChan <- fmt.Sprintf("%s", encodedMsg)
}

func (d *Driver) resourceManager() {
	OFPorts := make(map[string]OFPortContainer)
	AllPortDesc := make(map[string]map[uint32]string)

	for {
		select {
		case opMsg := <-d.dovesnapOpChan:
			switch opMsg.Operation {
			case "create":
				mustHandleCreateNetwork(d, opMsg)
			case "delete":
				mustHandleDeleteNetwork(d, opMsg)
			case "join":
				mustHandleJoinContainer(d, opMsg, &OFPorts)
			case "leave":
				mustHandleLeaveContainer(d, opMsg, &OFPorts)
			case "networks":
				mustHandleNetworks(d)
			default:
				log.Errorf("Unknown resource manager message: %s", opMsg)
			}
		case <-time.After(time.Second * 3):
			reconcileOvs(d, &AllPortDesc)
		}
	}
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

func (d *Driver) notifier() {
	for {
		select {
		case notifyMsg := <-d.notifyMsgChan:
			log.Debugf("%+v", notifyMsg)
			encodedMsg, err := json.Marshal(NotifyMsgJson{
				Version: 1,
				Time:    time.Now().Unix(),
				Msg:     notifyMsg,
			})
			if err != nil {
				panic(err)
			}
			// TODO: emit to UDS
			log.Infof(fmt.Sprintf("%s", encodedMsg))
		}
	}
}

func (d *Driver) restoreNetworks() {
	netlist := d.dockerer.mustGetNetworkList()
	for id, _ := range netlist {
		netInspect := d.dockerer.mustGetNetworkInspectFromID(id)
		ns, err := getNetworkStateFromResource(&netInspect)
		if err != nil {
			panic(err)
		}
		// TODO: verify dovesnap was restarted with the same arguments when restoring existing networks.
		d.networks[id] = ns
		sc := d.getStackMirrorConfigFromResource(&netInspect)
		d.stackMirrorConfigs[id] = sc
		log.Infof("restoring network %+v, %+v", ns, sc)
	}
}

func (d *Driver) getWebResponse(w http.ResponseWriter, operation string) {
	d.dovesnapOpChan <- DovesnapOp{Operation: operation}
	response := <-d.webResponseChan
	fmt.Fprintf(w, response)
}

func (d *Driver) handleNetworksWeb(w http.ResponseWriter, r *http.Request) {
	d.getWebResponse(w, "networks")
}

func (d *Driver) runWeb(port int) {
	http.HandleFunc("/networks", d.handleNetworksWeb)

	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), nil); err != nil {
		panic(err)
	}
}

func NewDriver(flagFaucetconfrpcClientName string, flagFaucetconfrpcServerName string, flagFaucetconfrpcServerPort int, flagFaucetconfrpcKeydir string, flagStackPriority1 string, flagStackingInterfaces string, flagStackMirrorInterface string, flagDefaultControllers string, flagMirrorBridgeIn string, flagMirrorBridgeOut string, flagStatusServerPort int) *Driver {
	ensureDirExists(netNsPath)

	stack_mirror_interface := strings.Split(flagStackMirrorInterface, ":")
	if len(flagStackMirrorInterface) > 0 && len(stack_mirror_interface) != 2 {
		panic(fmt.Errorf("Invalid stack mirror interface config: %s", flagStackMirrorInterface))
	}
	stacking_interfaces := strings.Split(flagStackingInterfaces, ",")
	log.Debugf("Stacking interfaces: %v", stacking_interfaces)

	d := &Driver{
		dockerer:                dockerer{},
		ovsdber:                 ovsdber{},
		faucetconfrpcer:         faucetconfrpcer{},
		stackPriority1:          flagStackPriority1,
		stackingInterfaces:      stacking_interfaces,
		stackMirrorInterface:    stack_mirror_interface,
		stackDefaultControllers: flagDefaultControllers,
		mirrorBridgeIn:          flagMirrorBridgeIn,
		mirrorBridgeOut:         flagMirrorBridgeOut,
		networks:                make(map[string]NetworkState),
		dovesnapOpChan:          make(chan DovesnapOp, 2),
		notifyMsgChan:           make(chan NotifyMsg, 2),
		webResponseChan:         make(chan string, 2),
		stackMirrorConfigs:      make(map[string]StackMirrorConfig),
	}

	d.dockerer.mustGetDockerClient()
	d.faucetconfrpcer.mustGetGRPCClient(flagFaucetconfrpcClientName, flagFaucetconfrpcServerName, flagFaucetconfrpcServerPort, flagFaucetconfrpcKeydir)

	d.ovsdber.waitForOvs()

	go d.notifier()

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

	d.restoreNetworks()

	go d.resourceManager()

	go d.runWeb(flagStatusServerPort)

	return d
}

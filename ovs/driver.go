package ovs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	networkplugin "github.com/docker/go-plugins-helpers/network"
	"github.com/iqtlabs/faucetconfrpc/faucetconfrpc"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

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
	udhcpcCmd        *exec.Cmd
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
	networks                map[string]NetworkState
	ofportmapChan           chan OFPortMap
	stackMirrorConfigs      map[string]StackMirrorConfig
}

// NetworkState is filled in at network creation time
// it contains state that we wish to keep for each network
type NetworkState struct {
	NetworkName       string
	BridgeName        string
	BridgeDpid        string
	BridgeDpidInt     int
	BridgeVLAN        int
	MTU               int
	Mode              string
	AddPorts          string
	Gateway           string
	GatewayMask       string
	FlatBindInterface string
	UseDHCP           bool
	Userspace         bool
}

func setFaucetConfigFile(confclient faucetconfserver.FaucetConfServerClient, config_yaml string) {
	log.Debugf("setFaucetConfigFile %s", config_yaml)
	req := &faucetconfserver.SetConfigFileRequest{
		ConfigYaml: config_yaml,
		Merge:      true,
	}
	_, err := confclient.SetConfigFile(context.Background(), req)
	if err != nil {
		panic(err)
	}
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
	err = d.ovsdber.createBridge(mirrorBridgeName, "", "", add_ports, true, false)
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

	err = d.ovsdber.createBridge(dpName, d.stackDefaultControllers, dpid, "", true, false)
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
		BridgeDpidInt:     mustGetIntDpid(dpid),
		BridgeVLAN:        vlan,
		MTU:               mtu,
		Mode:              mode,
		AddPorts:          add_ports,
		Gateway:           gateway,
		GatewayMask:       mask,
		FlatBindInterface: bindInterface,
		UseDHCP:           useDHCP,
		Userspace:         useUserspace,
	}

	d.networks[r.NetworkID] = ns
	d.stackMirrorConfigs[r.NetworkID] = d.getStackMirrorConfig(r)

	if operation == "create" {
		if err := d.initBridge(r.NetworkID, controller, dpid, add_ports, useUserspace); err != nil {
			panic(err)
		}
	}

	createmap := OFPortMap{
		OFPort:     0,
		AddPorts:   add_ports,
		Mode:       mode,
		NetworkID:  r.NetworkID,
		EndpointID: bridgeName,
		Operation:  operation,
	}

	d.ofportmapChan <- createmap
	return err
}

func (d *Driver) DeleteNetwork(r *networkplugin.DeleteNetworkRequest) error {
	log.Debugf("Delete network request: %+v", r)
	// remove the bridge from the faucet config if it exists
	ns := d.networks[r.NetworkID]
	log.Debugf("Deleting Bridge %s", ns.BridgeName)
	log.Debugf("Deleting DP %s from Faucet", ns.NetworkName)
	dp := []*faucetconfserver.DpInfo{
		{
			Name: ns.NetworkName,
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
		err := d.deletePatchPort(ns.BridgeName, mirrorBridgeName)
		if err != nil {
			log.Errorf("Unable to delete patch port to mirror bridge because: %v", err)
		}
	}

	if usingStacking(d) {
		_, stackDpName, _ := d.getStackDP()
		err := d.deletePatchPort(ns.BridgeName, stackDpName)
		if err != nil {
			log.Errorf("Unable to delete patch port between bridges because: %v", err)
		}
		if usingStackMirroring(d) {
			lbBridgeName := d.mustGetLoopbackDP()
			err = d.deletePatchPort(ns.BridgeName, lbBridgeName)
			if err != nil {
				log.Errorf("Unable to delete patch port to loopback bridge: %v", err)
			}
		}
	}

	_, err = d.deleteBridge(ns.BridgeName)
	if err != nil {
		log.Errorf("Deleting bridge %s failed: %s", ns.BridgeName, err)
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
	log.Debugf("Attached veth %+v,", r.Interface)
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
	ns := d.networks[r.NetworkID]
	ofport, _, err := d.addInternalPort(ns.BridgeName, localVethPair.Name, 0)
	if err != nil {
		log.Debugf("Error attaching veth [ %s ] to bridge [ %s ]", localVethPair.Name, ns.BridgeName)
		return nil, err
	}
	log.Infof("Attached veth [ %s ] to bridge [ %s ] ofport %d", localVethPair.Name, ns.BridgeName, ofport)

	// SrcName gets renamed to DstPrefix + ID on the container iface
	res := &networkplugin.JoinResponse{
		InterfaceName: networkplugin.InterfaceName{
			SrcName:   localVethPair.PeerName,
			DstPrefix: containerEthName,
		},
		Gateway: ns.Gateway,
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
	ns := d.networks[r.NetworkID]
	ofport, err := d.ovsdber.getOfPortNumber(portID)
	if err != nil {
		log.Errorf("Unable to get ofport number from %s", portID)
		return err
	}
	localVethPair := vethPair(truncateID(r.EndpointID))
	if err := netlink.LinkDel(localVethPair); err != nil {
		log.Errorf("Unable to delete veth on leave: %s", err)
	}
	err = d.ovsdber.deletePort(ns.BridgeName, portID)
	if err != nil {
		log.Errorf("OVS port [ %s ] delete transaction failed on bridge [ %s ] due to: %s", portID, ns.BridgeName, err)
		return err
	}
	log.Infof("Deleted OVS port [ %s ] from bridge [ %s ]", portID, ns.BridgeName)
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
	inspectNs, err := getNetworkStateFromResource(&netInspect)
	if err != nil {
		panic(err)
	}
	ns := d.networks[mapMsg.NetworkID]
	ns.NetworkName = inspectNs.NetworkName
	d.networks[mapMsg.NetworkID] = ns
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
			add_interfaces += fmt.Sprintf("%d: {description: %s, native_vlan: %d},", ofport, "Physical interface "+add_port, ns.BridgeVLAN)
		}
	}
	mode := mapMsg.Mode
	if mode == "nat" {
		add_interfaces += fmt.Sprintf("%d: {description: OVS Port for NAT, native_vlan: %d},", ofPortLocal, ns.BridgeVLAN)
	}
	configYaml := mergeInterfacesYaml(ns.NetworkName, ns.BridgeDpidInt, ns.BridgeName, add_interfaces)
	if usingMirrorBridge(d) {
		log.Debugf("configuring mirror bridge port for %s", ns.BridgeName)
		stackMirrorConfig := d.stackMirrorConfigs[mapMsg.NetworkID]
		ofportNum, mirrorOfportNum, err := d.addPatchPort(ns.BridgeName, mirrorBridgeName, uint(stackMirrorConfig.LbPort), 0)
		if err != nil {
			panic(err)
		}
		flowStr := fmt.Sprintf("priority=2,in_port=%d,actions=mod_vlan_vid:%d,output:1", mirrorOfportNum, ns.BridgeVLAN)
		mustOfCtl("add-flow", mirrorBridgeName, flowStr)
		add_interfaces += fmt.Sprintf("%d: {description: mirror, output_only: true},", ofportNum)
		configYaml = mergeInterfacesYaml(ns.NetworkName, ns.BridgeDpidInt, ns.BridgeName, add_interfaces)
	}
	if usingStacking(d) {
		_, stackDpName, err := d.getStackDP()
		if err != nil {
			panic(err)
		}
		ofportNum, ofportNumPeer, err := d.addPatchPort(ns.BridgeName, stackDpName, 0, 0)
		if err != nil {
			panic(err)
		}
		localDpYaml := fmt.Sprintf("%s: {dp_id: %d, description: %s, interfaces: {%s %d: {description: %s, stack: {dp: %s, port: %d}}}}",
			ns.NetworkName,
			ns.BridgeDpidInt,
			"OVS Bridge "+ns.BridgeName,
			add_interfaces,
			ofportNum,
			"Stack link to "+stackDpName,
			stackDpName,
			ofportNumPeer)
		remoteDpYaml := fmt.Sprintf("%s: {interfaces: {%d: {description: %s, stack: {dp: %s, port: %d}}}}",
			stackDpName,
			ofportNumPeer,
			"Stack link to "+ns.NetworkName,
			ns.NetworkName,
			ofportNum)
		configYaml = fmt.Sprintf("{dps: {%s, %s}}", localDpYaml, remoteDpYaml)
	}
	setFaucetConfigFile(d.faucetclient, configYaml)
	if usingStackMirroring(d) {
		lbBridgeName := d.mustGetLoopbackDP()
		stackMirrorConfig := d.stackMirrorConfigs[mapMsg.NetworkID]
		_, _, err = d.addPatchPort(ns.BridgeName, lbBridgeName, uint(stackMirrorConfig.LbPort), 0)
		if err != nil {
			panic(err)
		}
		req := &faucetconfserver.SetRemoteMirrorPortRequest{
			DpName:       ns.NetworkName,
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
	containerInspect, err := getContainerFromEndpoint(d.dockerclient, mapMsg.EndpointID)
	log.Debugf("%s", containerInspect)
	if err != nil {
		panic(err)
	}
	ns := d.networks[mapMsg.NetworkID]
	pid := containerInspect.State.Pid
	log.Infof("Adding %s (pid %d) on %s DPID %d OFPort %d to Faucet",
		containerInspect.Name, pid, ns.BridgeName, ns.BridgeDpidInt, mapMsg.OFPort)

	portacl := ""
	portacl, ok := containerInspect.Config.Labels["dovesnap.faucet.portacl"]
	if ok && len(portacl) > 0 {
		log.Infof("Set portacl %s on %s", portacl, containerInspect.Name)
	}
	add_interfaces := fmt.Sprintf("%d: {description: '%s', native_vlan: %d, acls_in: [%s]},",
		mapMsg.OFPort, fmt.Sprintf("%s %s", containerInspect.Name, truncateID(containerInspect.ID)), ns.BridgeVLAN, portacl)

	setFaucetConfigFile(d.faucetclient, mergeInterfacesYaml(ns.NetworkName, ns.BridgeDpidInt, ns.BridgeName, add_interfaces))

	mirror, ok := containerInspect.Config.Labels["dovesnap.faucet.mirror"]
	if ok && parseBool(mirror) {
		log.Infof("Mirroring container %s", containerInspect.Name)
		stackMirrorConfig := d.stackMirrorConfigs[mapMsg.NetworkID]
		if usingStackMirroring(d) || usingMirrorBridge(d) {
			req := &faucetconfserver.AddPortMirrorRequest{
				DpName:       ns.NetworkName,
				PortNo:       uint32(mapMsg.OFPort),
				MirrorPortNo: stackMirrorConfig.LbPort,
			}
			_, err := confclient.AddPortMirror(context.Background(), req)
			if err != nil {
				panic(err)
			}
		}
	}

	procPath := fmt.Sprintf("/proc/%d/ns/net", pid)
	procNetNsPath := fmt.Sprintf("%s/%s", netNsPath, containerInspect.ID)

	_, err = os.Lstat(procNetNsPath)
	if err == nil {
		log.Debugf("Remove existing %s", procNetNsPath)
		err = os.Remove(procNetNsPath)
		if err != nil {
			panic(err)
		}
	}

	err = os.Symlink(procPath, procNetNsPath)
	if err != nil {
		panic(err)
	}
	udhcpcCmd := exec.Command("ip", "netns", "exec", containerInspect.ID, "/sbin/udhcpc", "-f", "-R")
	if ns.UseDHCP {
		err = udhcpcCmd.Start()
		if err != nil {
			panic(err)
		}
		log.Infof("started udhcpc for %s", containerInspect.ID)
	} else {
		udhcpcCmd = nil
	}
	containerMap := OFPortContainer{
		OFPort:           mapMsg.OFPort,
		containerInspect: containerInspect,
		udhcpcCmd:        udhcpcCmd,
	}
	(*OFPorts)[mapMsg.EndpointID] = containerMap
}

func deleteDpInterface(confclient faucetconfserver.FaucetConfServerClient, dpName string, ofport uint32) {
	interfaces := &faucetconfserver.InterfaceInfo{
		PortNo: uint32(ofport),
	}
	interfacesConf := []*faucetconfserver.DpInfo{
		{
			Name:       dpName,
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
}

func mustHandleRm(d *Driver, confclient faucetconfserver.FaucetConfServerClient, mapMsg OFPortMap, OFPorts *map[string]OFPortContainer) {
	defer func() {
		if rerr := recover(); rerr != nil {
			log.Errorf("mustHandleRm failed: %v", rerr)
		}
	}()

	containerMap := (*OFPorts)[mapMsg.EndpointID]
	udhcpcCmd := containerMap.udhcpcCmd
	if udhcpcCmd != nil {
		log.Infof("Shutting down udhcpc")
		udhcpcCmd.Process.Kill()
		udhcpcCmd.Wait()
	}
	ns, have_network := d.networks[mapMsg.NetworkID]
	if !have_network {
		log.Debugf("Network %s already gone", mapMsg.NetworkID)
		delete(*OFPorts, mapMsg.EndpointID)
		return
	}

	log.Debugf("Removing port %d on %s from Faucet config", mapMsg.OFPort, ns.NetworkName)

	// TODO: faucetconfrpc should clean up the mirror reference.
	if usingStackMirroring(d) || usingMirrorBridge(d) {
		stackMirrorConfig := d.stackMirrorConfigs[mapMsg.NetworkID]
		req := &faucetconfserver.RemovePortMirrorRequest{
			DpName:       ns.NetworkName,
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

	deleteDpInterface(confclient, ns.NetworkName, uint32(mapMsg.OFPort))

	// The container will be gone by the time we query docker.
	delete(*OFPorts, mapMsg.EndpointID)
}

func reconcileOvs(d *Driver, allPortDesc *map[string]map[uint]string) {
	for id, ns := range d.networks {
		stackMirrorConfig := d.stackMirrorConfigs[id]
		newPortDesc := make(map[uint]string)
		err := scrapePortDesc(ns.BridgeName, &newPortDesc)
		if err != nil {
			continue
		}
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
				deleteDpInterface(d.faucetclient, ns.NetworkName, uint32(ofport))
			}
		} else {
			log.Debugf("new portDesc for %s", ns.BridgeName)
		}

		add_interfaces := ""

		for ofport, desc := range newPortDesc {
			// Ignore NAT and mirror port
			if uint32(ofport) == ofPortLocal || uint32(ofport) == stackMirrorConfig.LbPort {
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
			add_interfaces += fmt.Sprintf("%d: {description: %s, native_vlan: %d},", ofport, "Physical interface "+desc, ns.BridgeVLAN)
		}

		if add_interfaces != "" {
			configYaml := mergeInterfacesYaml(ns.NetworkName, ns.BridgeDpidInt, ns.BridgeName, add_interfaces)
			setFaucetConfigFile(d.faucetclient, configYaml)
		}

		(*allPortDesc)[id] = newPortDesc
	}
}

func consolidateDockerInfo(d *Driver, confclient faucetconfserver.FaucetConfServerClient) {
	OFPorts := make(map[string]OFPortContainer)
	AllPortDesc := make(map[string]map[uint]string)

	for {
		select {
		case mapMsg := <-d.ofportmapChan:
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
		default:
			reconcileOvs(d, &AllPortDesc)
			time.Sleep(3 * time.Second)
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

func restoreNetworks(d *Driver) {
	netlist, err := d.dockerclient.NetworkList(context.Background(), types.NetworkListOptions{})
	if err != nil {
		panic(fmt.Errorf("Could not get docker networks: %s", err))
	}
	for _, net := range netlist {
		if net.Driver == DriverName {
			netInspect, err := d.dockerclient.NetworkInspect(context.Background(), net.ID, types.NetworkInspectOptions{})
			if err != nil {
				panic(fmt.Errorf("Could not inspect docker network ID %s: %s", net.ID, err))
			}
			ns, parseerr := getNetworkStateFromResource(&netInspect)
			if parseerr != nil {
				panic(parseerr)
			}
			// TODO: verify dovesnap was restarted with the same arguments when restoring existing networks.
			d.networks[net.ID] = ns
			sc := d.getStackMirrorConfigFromResource(&netInspect)
			d.stackMirrorConfigs[net.ID] = sc
			log.Infof("restoring network %+v, %+v", ns, sc)
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
		networks:                make(map[string]NetworkState),
		ofportmapChan:           make(chan OFPortMap, 2),
		stackMirrorConfigs:      make(map[string]StackMirrorConfig),
	}

	d.ovsdber.waitForOvs()

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

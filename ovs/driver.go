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

	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	networkplugin "github.com/docker/go-plugins-helpers/network"
	"github.com/iqtlabs/faucetconfrpc/faucetconfrpc"
	bc "github.com/kenshaw/baseconv"
	"github.com/vishvananda/netlink"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	DriverName          = "ovs"
	defaultRoute        = "0.0.0.0/0"
	ovsPortPrefix       = "ovs-veth0-"
	bridgePrefix        = "ovsbr-"
	containerEthName    = "eth"
	bridgeAddPorts      = "ovs.bridge.add_ports"
	bridgeDpid          = "ovs.bridge.dpid"
	bridgeController    = "ovs.bridge.controller"
	vlanOption          = "ovs.bridge.vlan"
	mtuOption           = "ovs.bridge.mtu"
	defaultMTU          = 1500
	defaultVLAN         = 100
	bridgeNameOption    = "ovs.bridge.name"
	bindInterfaceOption = "ovs.bridge.bind_interface"
	modeOption          = "ovs.bridge.mode"
	modeNAT             = "nat"
	modeFlat            = "flat"
	defaultMode         = modeFlat
	ovsStartupRetries   = 5
	dockerRetries       = 3
	stackDpidPrefix     = "0x0E0F00"
	ofPortLocal         = 4294967294
)

var (
	validModes = map[string]bool{
		modeNAT:  true,
		modeFlat: true,
	}
)

type OFPortMap struct {
	OFPort     uint
	AddPorts   string
	Mode       string
	NetworkID  string
	EndpointID string
	Operation  string
}

type OFPortContainer struct {
	OFPort           uint
	containerInspect types.ContainerJSON
}

type Driver struct {
	dockerclient *client.Client
	ovsdber
	faucetclient       faucetconfserver.FaucetConfServerClient
	stackingInterfaces []string
	networks           map[string]*NetworkState
	ofportmapChan      chan OFPortMap
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

func (d *Driver) getStackDP() (string, string, error) {
	info, err := d.dockerclient.Info(context.Background())
	if err != nil {
		return "", "", err
	}
	engineId := strings.Split(info.ID, ":")[0]
	log.Debugf("Docker Engine ID %s:", info.ID)
	engineId = base36to16(engineId)
	dpid := stackDpidPrefix + engineId
	dpName := "dovesnap" + engineId
	return dpid, dpName, nil
}

func (d *Driver) createStackingBridge(r *networkplugin.CreateNetworkRequest) error {
	log.Debugf("Create stacking bridge request")

	controller := mustGetBridgeController(r)
	dpid, dpName, err := d.getStackDP()
	if err != nil {
		return err
	}

	// check if the stacking bridge already exists
	_, err = d.ovsdber.bridgeExists(dpName)
	if err == nil {
		log.Debugf("Stacking bridge already exists for this host")
		return nil
	} else {
		log.Infof("Stacking bridge doesn't exist, creating one now")
	}

	hostname, err := os.Hostname()
	if err != nil {
		return err
	}

	// TODO this needs to be validated, and looped through for multiples
	stackSlice := strings.Split(d.stackingInterfaces[0], ":")
	remoteDP := stackSlice[0]
	remotePort, err := strconv.ParseUint(stackSlice[1], 10, 32)
	if err != nil {
		log.Errorf("Unable to convert remote port to an unsigned integer because: [ %s ]", err)
	}
	localInterface := stackSlice[2]
	if err != nil {
		log.Errorf("Unable to convert local port to an unsigned integer because: [ %s ]", err)
	}

	err = d.ovsdber.createBridge(dpName, controller, dpid, "", true)
	if err != nil {
		log.Errorf("Unable to create stacking bridge because: [ %s ]", err)
	}

	// TODO for loop through stacking interfaces
	ofport, _, err := d.addInternalPort(dpName, localInterface, 0)
	if err != nil {
		log.Debugf("Error attaching veth [ %s ] to bridge [ %s ]", localInterface, dpName)
		return err
	}
	log.Infof("Attached veth [ %s ] to bridge [ %s ] ofport %d", localInterface, dpName, ofport)

	strDpid, _ := bc.Convert(strings.ToLower(dpid[2:]), bc.DigitsHex, bc.DigitsDec)
	intDpid, err := strconv.Atoi(strDpid)
	if err != nil {
		log.Errorf("Unable convert dp_id to an int because: %v", err)
		return err
	}

	sReq := &faucetconfserver.SetConfigFileRequest{
		ConfigYaml: fmt.Sprintf("{dps: {%s: {stack: {priority: 1}, interfaces: {%d: {description: %s, stack: {dp: %s, port: %d}}}}, %s: {dp_id: %d, description: %s, hardware: Open vSwitch, interfaces: {%d: {description: %s, stack: {dp: %s, port: %d}}}}}}",
			remoteDP,
			remotePort,
			"Stack link to "+dpName,
			dpName,
			ofport,
			dpName,
			intDpid,
			"Dovesnap Stacking Bridge for "+hostname,
			ofport,
			"Stack link to "+remoteDP,
			remoteDP,
			remotePort),
		Merge: true,
	}
	_, err = d.faucetclient.SetConfigFile(context.Background(), sReq)
	if err != nil {
		log.Errorf("Error while calling SetConfigFileRequest %s: %v", sReq, err)
	}

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

	log.Debugf("Initializing bridge for network %s", r.NetworkID)
	if err := d.initBridge(r.NetworkID, controller, dpid, add_ports); err != nil {
		panic(err)
	}

	if usingStacking(d) {
		stackerr := d.createStackingBridge(r)
		if stackerr != nil {
			panic(stackerr)
		}
	} else {
		log.Warnf("No stacking interface defined, not stacking DPs or creating a stacking bridge")
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
	_, err := d.deleteBridge(bridgeName)
	if err != nil {
		log.Errorf("Deleting bridge %s failed: %s", bridgeName, err)
		return err
	}

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

	_, err = d.faucetclient.DelDps(context.Background(), dReq)
	if err != nil {
		log.Errorf("Error while calling DelDps %s: %v", dReq, err)
		return err
	}

	delete(d.networks, r.NetworkID)

	if usingStacking(d) {
		_, stackDpName, err := d.getStackDP()
		if err != nil {
			log.Errorf("Unable to get stack DP name because: %v", err)
			return err
		}
		err = d.deletePatchPort(stackDpName, networkName)
		if err != nil {
			log.Errorf("Unable to delete patch port between bridges because: %v", err)
			return err
		}
	}
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
	req := &faucetconfserver.SetConfigFileRequest{
		ConfigYaml: fmt.Sprintf("{dps: {%s: {dp_id: %d, description: %s, interfaces: {%s}}}}",
			netInspect.Name,
			intDpid,
			"OVS Bridge "+bridgeName,
			add_interfaces),
		Merge: true,
	}
	if usingStacking(d) {
		_, stackDpName, err := d.getStackDP()
		if err != nil {
			panic(err)
		}
		ofportNum, ofportNumPeer, err := d.addPatchPort(
			bridgeName, stackDpName, netInspect.Name+"-patch-"+stackDpName, stackDpName+"-patch-"+netInspect.Name)
		if err != nil {
			panic(err)
		}
		req = &faucetconfserver.SetConfigFileRequest{
			ConfigYaml: fmt.Sprintf("{dps: {%s: {dp_id: %d, description: %s, interfaces: {%s %d: {description: %s, stack: {dp: %s, port: %d}}}}, %s: {interfaces: {%d: {description: %s, stack: {dp: %s, port: %d}}}}}}",
				netInspect.Name,
				intDpid,
				"OVS Bridge "+bridgeName,
				add_interfaces,
				ofportNum,
				"Stack link to "+stackDpName,
				stackDpName,
				ofportNumPeer,
				stackDpName,
				ofportNumPeer,
				"Stack link to "+netInspect.Name,
				netInspect.Name,
				ofportNum),
			Merge: true,
		}
	}
	_, err = d.faucetclient.SetConfigFile(context.Background(), req)
	if err != nil {
		panic(err)
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
	log.Infof("%s now on %s ofport %d", containerInspect.Name, bridgeName, mapMsg.OFPort)
	portacl, ok := containerInspect.Config.Labels["dovesnap.faucet.portacl"]
	if ok {
		log.Infof("Set portacl %s", portacl)
		req := &faucetconfserver.SetPortAclRequest{
			DpName: netInspect.Name,
			PortNo: uint32(mapMsg.OFPort),
			Acls:   portacl,
		}
		_, err := confclient.SetPortAcl(context.Background(), req)
		if err != nil {
			log.Errorf("Error while calling SetPortAcl RPC %s: %v", req, err)
		}
	}
	log.Debugf("Adding datapath %v to Faucet config", dpid)
	req := &faucetconfserver.SetConfigFileRequest{
		ConfigYaml: fmt.Sprintf("{dps: {%s: {dp_id: %d, interfaces: {%d: {description: %s, native_vlan: %d}}}}}",
			netInspect.Name, intDpid, mapMsg.OFPort, fmt.Sprintf("%s %s", containerInspect.Name, truncateID(containerInspect.ID)), vlan),
		Merge: true,
	}
	_, err = confclient.SetConfigFile(context.Background(), req)
	if err != nil {
		panic(err)
	}
}

func mustHandleRm(d *Driver, confclient faucetconfserver.FaucetConfServerClient, mapMsg OFPortMap, OFPorts *map[string]OFPortContainer) {
	defer func() {
		if rerr := recover(); rerr != nil {
			log.Errorf("mustHandleRm failed: %v", rerr)
		}
	}()
	networkName := mustGetNetworkNameFromID(d.dockerclient, mapMsg.NetworkID)
	interfaces := &faucetconfserver.InterfaceInfo{
		PortNo: int32(mapMsg.OFPort),
	}

	log.Debugf("Removing port %d on %s from Faucet config", mapMsg.OFPort, networkName)
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

func mustGetGRPCClient(flagFaucetconfrpcServerName string, flagFaucetconfrpcServerPort int, flagFaucetconfrpcKeydir string) *grpc.ClientConn {
	crt_file := flagFaucetconfrpcKeydir + "/client.crt"
	key_file := flagFaucetconfrpcKeydir + "/client.key"
	ca_file := flagFaucetconfrpcKeydir + "/" + flagFaucetconfrpcServerName + "-ca.crt"
	certificate, err := tls.LoadX509KeyPair(crt_file, key_file)
	if err != nil {
		panic(err)
	}
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
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(creds), grpc.WithBlock())
	if err != nil {
		panic(err)
	}
	return conn
}

func usingStacking(d *Driver) bool {
	return len(d.stackingInterfaces[0]) != 0
}

func NewDriver(flagFaucetconfrpcServerName string, flagFaucetconfrpcServerPort int, flagFaucetconfrpcKeydir string, flagStackingInterfaces string) (*Driver, error) {
	// Get interfaces to use for stacking
	stacking_interfaces := strings.Split(flagStackingInterfaces, ",")
	log.Debugf("Stacking interfaces: %v", stacking_interfaces)
	conn := mustGetGRPCClient(flagFaucetconfrpcServerName, flagFaucetconfrpcServerPort, flagFaucetconfrpcKeydir)
	confclient := faucetconfserver.NewFaucetConfServerClient(conn)

	// Connect to Docker
	docker, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("Could not connect to docker: %s", err)
	}

	// Create Docker driver
	d := &Driver{
		dockerclient:       docker,
		ovsdber:            ovsdber{},
		faucetclient:       confclient,
		stackingInterfaces: stacking_interfaces,
		networks:           make(map[string]*NetworkState),
		ofportmapChan:      make(chan OFPortMap, 2),
	}

	for i := 0; i < ovsStartupRetries; i++ {
		_, err = d.ovsdber.show()
		if err == nil {
			break
		}
		log.Infof("Waiting for open vswitch")
		time.Sleep(5 * time.Second)
	}
	_, err = d.ovsdber.show()
	if err != nil {
		return nil, fmt.Errorf("Could not connect to open vswitch")
	}

	go consolidateDockerInfo(d, confclient)

	netlist, err := d.dockerclient.NetworkList(context.Background(), types.NetworkListOptions{})
	if err != nil {
		return nil, fmt.Errorf("Could not get docker networks: %s", err)
	}
	for _, net := range netlist {
		if net.Driver == DriverName {
			netInspect, err := d.dockerclient.NetworkInspect(context.Background(), net.ID, types.NetworkInspectOptions{})
			if err != nil {
				return nil, fmt.Errorf("Could not inpect docker networks inpect: %s", err)
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
	return d, nil
}

// Create veth pair. Peername is renamed to eth0 in the container
func vethPair(suffix string) *netlink.Veth {
	return &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: ovsPortPrefix + suffix},
		PeerName:  "ethc" + suffix,
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

func mustGetBridgeMTU(r *networkplugin.CreateNetworkRequest) int {
	bridgeMTU := defaultMTU
	mtu := getGenericOption(r, mtuOption)
	if mtu != "" {
		mtu_int, err := strconv.Atoi(mtu)
		if err != nil {
			bridgeMTU = mtu_int
		}
	}
	return bridgeMTU
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
	bridgeVLAN := defaultVLAN
	vlan := getGenericOption(r, vlanOption)
	if vlan != "" {
		vlan_int, err := strconv.Atoi(vlan)
		if err != nil {
			bridgeVLAN = vlan_int
		}
	}
	return bridgeVLAN
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
		panic(fmt.Errorf("No gateway IP found"))
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

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
	"github.com/cyberreboot/faucetconfrpc/faucetconfrpc"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	networkplugin "github.com/docker/go-plugins-helpers/network"
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
	dpid := "0x0E0F00" + engineId
	dpName := "dovesnap" + engineId
	return dpid, dpName, nil
}

func (d *Driver) createStackingBridge(r *networkplugin.CreateNetworkRequest) error {
	log.Debugf("Create stacking bridge request")

	controller, err := getBridgeController(r)
	if err != nil {
		return err
	}

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
	if len(d.stackingInterfaces[0]) == 0 {
		log.Warnf("No stacking interface defined, not stacking DPs or creating a stacking bridge")
		return nil
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

func (d *Driver) CreateNetwork(r *networkplugin.CreateNetworkRequest) error {
	log.Debugf("Create network request: %+v", r)

	bridgeName, err := getBridgeName(r)
	if err != nil {
		return err
	}

	mtu, err := getBridgeMTU(r)
	if err != nil {
		return err
	}

	mode, err := getBridgeMode(r)
	if err != nil {
		return err
	}

	gateway, mask, err := getGatewayIP(r)
	if err != nil {
		return err
	}

	bindInterface, err := getBindInterface(r)
	if err != nil {
		return err
	}

	controller, err := getBridgeController(r)
	if err != nil {
		return err
	}

	dpid, err := getBridgeDpid(r)
	if err != nil {
		return err
	}

	vlan, err := getBridgeVLAN(r)
	if err != nil {
		return err
	}

	add_ports, err := getBridgeAddPorts(r)
	if err != nil {
		return err
	}

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
		delete(d.networks, r.NetworkID)
		return err
	}

	// Create stacking bridge and links
	stackerr := d.createStackingBridge(r)
	if stackerr != nil {
		log.Errorf("Unable to create stacking bridge because: [ %s ]", stackerr)
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
	return nil
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
	// TODO can't get the name this way because the network is already deleted
	netInspect, err := getNetworkInspectFromID(d.dockerclient, r.NetworkID)
	if err != nil {
		log.Errorf("Unable to get network inspection because: %v", err)
		return err
	}
	log.Debugf("Deleting DP %s from Faucet", netInspect.Name)
	// TODO remove the bridge from the faucet config if it exists
	delete(d.networks, r.NetworkID)
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

func getNetworkNameFromID(dockerclient *client.Client, NetworkID string) (string, error) {
	for i := 0; i < dockerRetries; i++ {
		netInspect, err := dockerclient.NetworkInspect(context.Background(), NetworkID, types.NetworkInspectOptions{})
		if err == nil {
			return netInspect.Name, nil
		}
		time.Sleep(1 * time.Second)
	}
	return "", fmt.Errorf("Network %s not found", NetworkID)
}

func getNetworkInspectFromID(dockerclient *client.Client, NetworkID string) (types.NetworkResource, error) {
	for i := 0; i < dockerRetries; i++ {
		netInspect, err := dockerclient.NetworkInspect(context.Background(), NetworkID, types.NetworkInspectOptions{})
		if err == nil {
			return netInspect, nil
		}
		time.Sleep(1 * time.Second)
	}
	return types.NetworkResource{}, fmt.Errorf("Network %s not found", NetworkID)
}

func consolidateDockerInfo(d *Driver, confclient faucetconfserver.FaucetConfServerClient) {
	OFPorts := make(map[string]OFPortContainer)

	for {
		mapMsg := <-d.ofportmapChan
		switch mapMsg.Operation {
		case "create":
			{
				_, stackDpName, err := d.getStackDP()
				if err != nil {
					log.Errorf("Unable to get stack DP name because: %v", err)
					break
				}
				log.Debugf("network id: %s", mapMsg.NetworkID)
				bridgeName := bridgePrefix + truncateID(mapMsg.NetworkID)
				netInspect, err := getNetworkInspectFromID(d.dockerclient, mapMsg.NetworkID)
				if err != nil {
					log.Errorf("Unable to get network inspection because: %v", err)
					break
				}
				dpid, err := getBridgeDpidfromresource(&netInspect)
				if err != nil {
					log.Errorf("Unable to get bridge dp_id because: %v", err)
					break
				}
				vlan, err := getBridgeVlanfromresource(&netInspect)
				if err != nil {
					log.Errorf("Unable to get bridge vlan because: %v", err)
					break
				}

				ofportNum, ofportNumPeer, err := d.addPatchPort(bridgeName, stackDpName, netInspect.Name+"-patch-"+stackDpName, stackDpName+"-patch-"+netInspect.Name)
				if err != nil {
					log.Errorf("Unable to create patch port between bridges because: %v", err)
					break
				}

				strDpid, _ := bc.Convert(strings.ToLower(dpid[2:]), bc.DigitsHex, bc.DigitsDec)
				intDpid, err := strconv.Atoi(strDpid)
				if err != nil {
					log.Errorf("Unable convert dp_id to an int because: %v", err)
					break
				}

				add_ports := mapMsg.AddPorts
				add_interfaces := ""
				if add_ports != "" {
			                for _, add_port_number_str := range strings.Split(add_ports, ",") {
						add_port_number := strings.Split(add_port_number_str, "/")
						add_port := add_port_number[0]
						ofport, err := d.ovsdber.getOfPortNumber(add_port)
						if err != nil {
							log.Errorf("Unable to get ofport number from %s", add_port)
							break
						}
						add_interfaces += fmt.Sprintf("%d: {description: %s, native_vlan: %d},", ofport, "Physical interface " + add_port, vlan)
					}
				}
				mode := mapMsg.Mode
				if mode == "nat" {
					add_interfaces += fmt.Sprintf("4294967294: {description: OVS Port for NAT, native_vlan: %d},", vlan)
				}

				sReq := &faucetconfserver.SetConfigFileRequest{
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
				_, err = d.faucetclient.SetConfigFile(context.Background(), sReq)
				if err != nil {
					log.Errorf("Error while calling SetConfigFileRequest %s: %v", sReq, err)
				}
			}
		case "add":
			{
				containerInspect, netInspect, err := getContainerFromEndpoint(d.dockerclient, mapMsg.EndpointID)
				if err == nil {
					bridgeName, _ := getBridgeNamefromresource(&netInspect)
					dpid, err := getBridgeDpidfromresource(&netInspect)
					if err != nil {
						log.Errorf("No bridge DPID could be found, can't set Faucet config")
						break
					}
					vlan, err := getBridgeVlanfromresource(&netInspect)
					if err != nil {
						log.Errorf("No bridge VLAN could be found, can't set Faucet config")
						break
					}
					OFPorts[mapMsg.EndpointID] = OFPortContainer{
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
					strDpid, _ := bc.Convert(strings.ToLower(dpid[2:]), bc.DigitsHex, bc.DigitsDec)
					intDpid, err := strconv.Atoi(strDpid)
					if err != nil {
						log.Errorf("Unable convert dp_id to an int because: %v", err)
						break
					}

					req := &faucetconfserver.SetConfigFileRequest{
						ConfigYaml: fmt.Sprintf("{dps: {%s: {dp_id: %d, interfaces: {%d: {description: %s, native_vlan: %d}}}}}",
							netInspect.Name, intDpid, mapMsg.OFPort, fmt.Sprintf("%s %s", containerInspect.Name, truncateID(containerInspect.ID)), vlan),
						Merge: true,
					}
					_, err = confclient.SetConfigFile(context.Background(), req)
					if err != nil {
						log.Errorf("Error while calling SetConfigFileRequest %s: %v", req, err)
					}
				}
			}
		case "rm":
			{
				networkName, err := getNetworkNameFromID(d.dockerclient, mapMsg.NetworkID)
				if err != nil {
					log.Errorf("Unable to find Docker network %s to remove it from Faucet", mapMsg.NetworkID)
				} else {
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
					delete(OFPorts, mapMsg.EndpointID)
				}
			}
		default:
			log.Errorf("Unknown consolidation message: %s", mapMsg)
		}
	}
}

func NewDriver(flagFaucetconfrpcServerName string, flagFaucetconfrpcServerPort int, flagFaucetconfrpcKeydir string, flagStackingInterfaces string) (*Driver, error) {
	// Get interfaces to use for stacking
	stacking_interfaces := strings.Split(flagStackingInterfaces, ",")
	log.Debugf("Stacking interfaces: %v", stacking_interfaces)

	// Read faucetconfrpc credentials.
	crt_file := flagFaucetconfrpcKeydir + "/client.crt"
	key_file := flagFaucetconfrpcKeydir + "/client.key"
	ca_file := flagFaucetconfrpcKeydir + "/" + flagFaucetconfrpcServerName + "-ca.crt"
	certificate, err := tls.LoadX509KeyPair(crt_file, key_file)
	if err != nil {
		log.Fatalf("Could not load client key pair %s, %s: %s", crt_file, key_file, err)
	}
	certPool := x509.NewCertPool()
	ca, err := ioutil.ReadFile(ca_file)
	if err != nil {
		log.Fatalf("Could not read ca certificate %s: %s", ca_file, err)
	}
	if ok := certPool.AppendCertsFromPEM(ca); !ok {
		log.Fatalf("Failed to append ca certs")
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
		log.Fatalf("Could not dial %s: %s", addr, err)
	}
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
			bridgeName, err := getBridgeNamefromresource(&netInspect)
			if err != nil {
				return nil, err
			}
			ns := &NetworkState{
				BridgeName: bridgeName,
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

func getBridgeMTU(r *networkplugin.CreateNetworkRequest) (int, error) {
	bridgeMTU := defaultMTU
	mtu := getGenericOption(r, mtuOption)
	if mtu != "" {
		mtu_int, err := strconv.Atoi(mtu)
		if err != nil {
			bridgeMTU = mtu_int
		}
	}
	return bridgeMTU, nil
}

func getBridgeName(r *networkplugin.CreateNetworkRequest) (string, error) {
	bridgeName := bridgePrefix + truncateID(r.NetworkID)
	name := getGenericOption(r, bridgeNameOption)
	if name != "" {
		bridgeName = name
	}
	return bridgeName, nil
}

func getBridgeMode(r *networkplugin.CreateNetworkRequest) (string, error) {
	bridgeMode := defaultMode
	mode := getGenericOption(r, modeOption)
	if mode != "" {
		if _, isValid := validModes[mode]; !isValid {
			return "", fmt.Errorf("%s is not a valid mode", mode)
		}
		bridgeMode = mode
	}
	return bridgeMode, nil
}

func getBridgeController(r *networkplugin.CreateNetworkRequest) (string, error) {
	return getGenericOption(r, bridgeController), nil
}

func getBridgeDpid(r *networkplugin.CreateNetworkRequest) (string, error) {
	return getGenericOption(r, bridgeDpid), nil
}

func getBridgeVLAN(r *networkplugin.CreateNetworkRequest) (int, error) {
	bridgeVLAN := defaultVLAN
	vlan := getGenericOption(r, vlanOption)
	if vlan != "" {
		vlan_int, err := strconv.Atoi(vlan)
		if err != nil {
			bridgeVLAN = vlan_int
		}
	}
	return bridgeVLAN, nil
}

func getBridgeAddPorts(r *networkplugin.CreateNetworkRequest) (string, error) {
	return getGenericOption(r, bridgeAddPorts), nil
}

func getGatewayIP(r *networkplugin.CreateNetworkRequest) (string, string, error) {
	// FIXME: Dear future self, I'm sorry for leaving you with this mess, but I want to get this working ASAP
	// This should be an array
	// We need to handle case where we have
	// a. v6 and v4 - dual stack
	// auxilliary address
	// multiple subnets on one network
	// also in that case, we'll need a function to determine the correct default gateway based on it's IP/Mask
	var gatewayIP string

	if len(r.IPv6Data) > 0 {
		if r.IPv6Data[0] != nil {
			if r.IPv6Data[0].Gateway != "" {
				gatewayIP = r.IPv6Data[0].Gateway
			}
		}
	}
	// Assumption: IPAM will provide either IPv4 OR IPv6 but not both
	// We may want to modify this in future to support dual stack
	if len(r.IPv4Data) > 0 {
		if r.IPv4Data[0] != nil {
			if r.IPv4Data[0].Gateway != "" {
				gatewayIP = r.IPv4Data[0].Gateway
			}
		}
	}

	if gatewayIP == "" {
		return "", "", fmt.Errorf("No gateway IP found")
	}
	parts := strings.Split(gatewayIP, "/")
	if parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("Cannot split gateway IP address")
	}
	return parts[0], parts[1], nil
}

func getBindInterface(r *networkplugin.CreateNetworkRequest) (string, error) {
	if r.Options != nil {
		if mode, ok := r.Options[bindInterfaceOption].(string); ok {
			return mode, nil
		}
	}
	// As bind interface is optional and has no default, don't return an error
	return "", nil
}

func getBridgeNamefromresource(r *types.NetworkResource) (string, error) {
	bridgeName := bridgePrefix + truncateID(r.ID)
	return bridgeName, nil
}

func getBridgeDpidfromresource(r *types.NetworkResource) (string, error) {
	if r.Options != nil {
		if dpid, ok := r.Options[bridgeDpid]; ok {
			return dpid, nil
		}
	}
	return "", fmt.Errorf("No DPID found for this network")
}

func getBridgeVlanfromresource(r *types.NetworkResource) (int, error) {
	if r.Options != nil {
		vlan, err := strconv.Atoi(r.Options[vlanOption])
		if err == nil {
			return vlan, nil
		}
	}
	log.Infof("No VLAN found for this network, using default: %d", defaultVLAN)
	return defaultVLAN, nil
}

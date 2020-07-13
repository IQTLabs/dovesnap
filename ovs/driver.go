package ovs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"fmt"
	"strconv"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	networkplugin "github.com/docker/go-plugins-helpers/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/api/types"
	"github.com/vishvananda/netlink"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"github.com/cyberreboot/faucetconfrpc/faucetconfrpc"
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
	mtuOption           = "ovs.bridge.mtu"
	defaultMTU          = 1500
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
	OFPort uint
	NetworkID string
	EndpointID string
	Operation string
}

type OFPortContainer struct {
	OFPort uint
	containerInspect types.ContainerJSON
}

type Driver struct {
	dockerclient *client.Client
	ovsdber
	networks map[string]*NetworkState
	ofportmapChan chan OFPortMap
}

// NetworkState is filled in at network creation time
// it contains state that we wish to keep for each network
type NetworkState struct {
	BridgeName        string
	MTU               int
	Mode              string
	Gateway           string
	GatewayMask       string
	FlatBindInterface string
}

func getGenericOption(r *networkplugin.CreateNetworkRequest, optionName string) (string) {
	if r.Options == nil {
		return ""
	}
	optionsMap, have_options := r.Options["com.docker.network.generic"].(map[string]interface{})
	if ! have_options {
		return ""
	}
	optionValue, have_option := optionsMap[optionName].(string)
	if ! have_option {
		return ""
	}
	return optionValue
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

	add_ports, err := getBridgeAddPorts(r)
	if err != nil {
		return err
	}

	ns := &NetworkState{
		BridgeName:        bridgeName,
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
	return nil
}

func (d *Driver) DeleteNetwork(r *networkplugin.DeleteNetworkRequest) error {
	log.Debugf("Delete network request: %+v", r)
	bridgeName := d.networks[r.NetworkID].BridgeName
	log.Debugf("Deleting Bridge %s", bridgeName)
	err := d.deleteBridge(bridgeName)
	if err != nil {
		log.Errorf("Deleting bridge %s failed: %s", bridgeName, err)
		return err
	}
	delete(d.networks, r.NetworkID)
	return nil
}

func (d *Driver) CreateEndpoint(r *networkplugin.CreateEndpointRequest) (*networkplugin.CreateEndpointResponse,error) {
	log.Debugf("Create endpoint request: %+v", r)
	localVethPair := vethPair(truncateID(r.EndpointID))
	log.Debugf("Create vethPair")
	res := &networkplugin.CreateEndpointResponse{Interface: &networkplugin.EndpointInterface{MacAddress: localVethPair.Attrs().HardwareAddr.String()}}
	log.Debugf("Attached veth5 %+v," ,r.Interface)
	return res,nil
}

func (d *Driver) GetCapabilities () (*networkplugin.CapabilitiesResponse,error) {
	log.Debugf("Get capabilities request")
	res := &networkplugin.CapabilitiesResponse{
		Scope: "local",
	}
	return res,nil
}

func (d *Driver) ProgramExternalConnectivity (r *networkplugin.ProgramExternalConnectivityRequest) error {
	log.Debugf("Program external connectivity request: %+v", r)
	return nil
}

func (d *Driver) RevokeExternalConnectivity (r *networkplugin.RevokeExternalConnectivityRequest) error {
	log.Debugf("Revoke external connectivity request: %+v", r)
	return nil
}

func (d *Driver) FreeNetwork (r *networkplugin.FreeNetworkRequest) error {
	log.Debugf("Free network request: %+v", r)
	return nil
}

func (d *Driver) DiscoverNew (r *networkplugin.DiscoveryNotification) error {
	log.Debugf("Discover new request: %+v", r)
	return nil
}

func (d *Driver) DiscoverDelete (r *networkplugin.DiscoveryNotification) error {
        log.Debugf("Discover delete request: %+v", r)
        return nil
}

func (d *Driver) DeleteEndpoint(r *networkplugin.DeleteEndpointRequest) error {
	log.Debugf("Delete endpoint request: %+v", r)
	return nil
}

func (d *Driver) AllocateNetwork(r *networkplugin.AllocateNetworkRequest) (*networkplugin.AllocateNetworkResponse,error) {
	log.Debugf("Allocate network request: %+v", r)
	res := &networkplugin.AllocateNetworkResponse{
		Options: make(map[string]string),
	}
	return res,nil
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
	ofport, err := d.addInternalPort(bridgeName, localVethPair.Name, 0)
	if err != nil {
		log.Errorf("error attaching veth [ %s ] to bridge [ %s ]", localVethPair.Name, bridgeName)
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
		OFPort: ofport,
		NetworkID: r.NetworkID,
		EndpointID: r.EndpointID,
		Operation: "add",
	}
	d.ofportmapChan <-addmap
	return res, nil
}

func (d *Driver) Leave(r *networkplugin.LeaveRequest) error {
	log.Debugf("Leave request: %+v", r)
	localVethPair := vethPair(truncateID(r.EndpointID))
	if err := netlink.LinkDel(localVethPair); err != nil {
		log.Errorf("unable to delete veth on leave: %s", err)
	}
	portID := fmt.Sprintf(ovsPortPrefix + truncateID(r.EndpointID))
	bridgeName := d.networks[r.NetworkID].BridgeName
	err := d.ovsdber.deletePort(bridgeName, portID)
	if err != nil {
		log.Errorf("OVS port [ %s ] delete transaction failed on bridge [ %s ] due to: %s", portID, bridgeName, err)
		return err
	}
	log.Infof("Deleted OVS port [ %s ] from bridge [ %s ]", portID, bridgeName)
	log.Debugf("Leave %s:%s", r.NetworkID, r.EndpointID)
	rmmap := OFPortMap{
		OFPort: 0,
		NetworkID: r.NetworkID,
		EndpointID: r.EndpointID,
		Operation: "rm",
	}
	d.ofportmapChan <-rmmap
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
			if (err != nil) {
				continue
			}
			for containerID, containerInfo := range netInspect.Containers {
				if containerInfo.EndpointID == EndpointID {
					containerInspect, err := dockerclient.ContainerInspect(context.Background(), containerID)
					if (err != nil) {
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

func consolidateDockerInfo(d *Driver, confclient faucetconfserver.FaucetConfServerClient) () {
	OFPorts := make(map[string]OFPortContainer)

	for {
		mapMsg := <-d.ofportmapChan
		switch mapMsg.Operation {
			case "add": {
				containerInspect, netInspect, err := getContainerFromEndpoint(d.dockerclient, mapMsg.EndpointID)
				if err == nil {
					bridgeName, _ := getBridgeNamefromresource(&netInspect)
					OFPorts[mapMsg.EndpointID] = OFPortContainer{
						OFPort: mapMsg.OFPort,
						containerInspect: containerInspect,
					}
					log.Infof("%s now on %s ofport %d", containerInspect.Name, bridgeName, mapMsg.OFPort)
					portacl, ok := containerInspect.Config.Labels["dovesnap.faucet.portacl"]
					if (ok) {
						log.Infof("Set portacl %s", portacl)
						req := &faucetconfserver.SetPortAclRequest{
							DpName: netInspect.Name,
							PortNo: int32(mapMsg.OFPort),
							Acls: portacl,
						}
						_, err := confclient.SetPortAcl(context.Background(), req)
						if err != nil {
							log.Errorf("error while calling SetPortAcl RPC %s: %v", req, err)
						}
					}
					req := &faucetconfserver.SetConfigFileRequest{
						ConfigYaml: fmt.Sprintf("{dps: {%s: {interfaces: {%d: {description: %s}}}}}",
							netInspect.Name, mapMsg.OFPort, containerInspect.Name),
						Merge: true,
					}
					_, err = confclient.SetConfigFile(context.Background(), req)
					if err != nil {
						log.Errorf("error while calling SetConfigFileRequest %s: %v", req, err)
					}
				}
			}
			case "rm": {
				// The container will be gone by the time we query docker.
				delete (OFPorts, mapMsg.EndpointID)
			}
			default:
				log.Errorf("unknown consolidation message: %s", mapMsg)
		}
	}
}

func NewDriver(flagFaucetconfrpcServerName string, flagFaucetconfrpcServerPort int, flagFaucetconfrpcKeydir string) (*Driver, error) {
	// Read faucetconfrpc credentials.
	crt_file := flagFaucetconfrpcKeydir + "/client.crt"
	key_file := flagFaucetconfrpcKeydir + "/client.key"
	ca_file := flagFaucetconfrpcKeydir + "/" + flagFaucetconfrpcServerName + "-ca.crt"
	certificate, err := tls.LoadX509KeyPair(crt_file, key_file)
	if err != nil {
		log.Fatalf("could not load client key pair %s, %s: %s", crt_file, key_file, err)
	}
	certPool := x509.NewCertPool()
	ca, err := ioutil.ReadFile(ca_file)
	if err != nil {
		log.Fatalf("could not read ca certificate %s: %s", ca_file, err)
	}
	if ok := certPool.AppendCertsFromPEM(ca); !ok {
		log.Fatalf("failed to append ca certs")
	}
	creds := credentials.NewTLS(&tls.Config{
		ServerName: flagFaucetconfrpcServerName,
		Certificates: []tls.Certificate{certificate},
		RootCAs: certPool,
	})
	// Connect to faucetconfrpc server.
	addr := flagFaucetconfrpcServerName + ":" + strconv.Itoa(flagFaucetconfrpcServerPort)
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(creds), grpc.WithBlock())
	if err != nil {
		log.Fatalf("could not dial %s: %s", addr, err)
	}
	confclient := faucetconfserver.NewFaucetConfServerClient(conn)

	// Connect to Docker
	docker, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("could not connect to docker: %s", err)
	}

	// Create Docker driver
	d := &Driver{
		dockerclient: docker,
		ovsdber: ovsdber{},
		networks: make(map[string]*NetworkState),
		ofportmapChan: make(chan OFPortMap, 2),
	}

	// Connect to OVSDB
	for i := 0; i < ovsStartupRetries; i++ {
		err = d.ovsdber.show()
		if err == nil {
			break
		}
		log.Infof("Waiting for open vswitch")
		time.Sleep(5 * time.Second)
	}
	if d.ovsdber.show() != nil {
		return nil, fmt.Errorf("Could not connect to open vswitch")
	}

	go consolidateDockerInfo(d, confclient)

	netlist, err := d.dockerclient.NetworkList(context.Background(), types.NetworkListOptions{})
	if err != nil {
		return nil, fmt.Errorf("Could not get docker networks: %s", err)
	}
	for _, net := range netlist{
		if net.Driver == DriverName{
			netInspect, err := d.dockerclient.NetworkInspect(context.Background(), net.ID, types.NetworkInspectOptions{})
			if err != nil {
				return nil, fmt.Errorf("Could not inpect docker networks inpect: %s", err)
			}
			bridgeName, err := getBridgeNamefromresource(&netInspect)
			if err != nil {
				return nil,err
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
		PeerName: "ethc" + suffix,
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

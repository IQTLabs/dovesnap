package ovs

import (
	"fmt"
	"strings"
	"time"

	"github.com/docker/libnetwork/iptables"
	log "github.com/sirupsen/logrus"
)

func (ovsdber *ovsdber) show() (string, error) {
	return VsCtl("show")
}

func (ovsdber *ovsdber) waitForOvs() {
	for i := 0; i < ovsStartupRetries; i++ {
		_, err := ovsdber.show()
		if err == nil {
			break
		}
		log.Infof("Waiting for open vswitch")
		time.Sleep(5 * time.Second)
	}
	_, err := ovsdber.show()
	if err != nil {
		panic(fmt.Errorf("Could not connect to open vswitch"))
	}
	log.Infof("Connected to open vswitch")
}

// checks if a bridge already exists
func (ovsdber *ovsdber) bridgeExists(bridgeName string) (string, error) {
	return VsCtl("br-exists", bridgeName)
}

// addBridge adds the OVS bridge
func (ovsdber *ovsdber) addBridge(bridgeName string) (string, error) {
	return VsCtl("add-br", bridgeName, "--", "set", "Bridge", bridgeName, "stp_enable=false")
}

// addBridgeExists adds the OVS bridge or does nothing if it already exists
func (ovsdber *ovsdber) addBridgeExists(bridgeName string) (string, error) {
	return VsCtl("--may-exist", "add-br", bridgeName, "--", "set", "Bridge", bridgeName, "stp_enable=false")
}

// deleteBridge deletes the OVS bridge
func (ovsdber *ovsdber) deleteBridge(bridgeName string) (string, error) {
	return VsCtl("del-br", bridgeName)
}

func (ovsdber *ovsdber) makeMirrorBridge(bridgeName string, mirrorBridgeOutPort uint) {
	mustOfCtl("del-flows", bridgeName)
	mustOfCtl("add-flow", bridgeName, "priority=0,actions=drop")
	mustOfCtl("add-flow", bridgeName, fmt.Sprintf("priority=1,actions=output:%d", mirrorBridgeOutPort))
}

func (ovsdber *ovsdber) makeLoopbackBridge(bridgeName string) (err error) {
	err = nil
	defer func() {
		if rerr := recover(); rerr != nil {
			err = fmt.Errorf("Cannot makeLoopbackBridge: %v", rerr)
		}
	}()

	mustOfCtl("del-flows", bridgeName)
	mustOfCtl("add-flow", bridgeName, "priority=0,actions=drop")
	mustOfCtl("add-flow", bridgeName, "priority=1,actions=output:in_port")
	return err
}

func (ovsdber *ovsdber) createBridge(bridgeName string, controller string, dpid string, add_ports string, exists bool) error {
	if exists {
		if _, err := ovsdber.addBridgeExists(bridgeName); err != nil {
			log.Errorf("Error creating ovs bridge [ %s ] : [ %s ]", bridgeName, err)
			return err
		}
	} else {
		if _, err := ovsdber.addBridge(bridgeName); err != nil {
			log.Errorf("Error creating ovs bridge [ %s ] : [ %s ]", bridgeName, err)
			return err
		}
	}
	var ovsConfigCmds [][]string

	if dpid != "" {
		ovsConfigCmds = append(ovsConfigCmds, []string{"set", "bridge", bridgeName, fmt.Sprintf("other-config:datapath-id=%s", dpid)})
	}

	if controller != "" {
		ovsConfigCmds = append(ovsConfigCmds, []string{"set", "bridge", bridgeName, "fail-mode=secure"})
		controllers := append([]string{"set-controller", bridgeName}, strings.Split(controller, ",")...)
		ovsConfigCmds = append(ovsConfigCmds, controllers)
	}

	if add_ports != "" {
		for _, add_port_number_str := range strings.Split(add_ports, ",") {
			add_port_number := strings.Split(add_port_number_str, "/")
			add_port := add_port_number[0]
			if len(add_port_number) == 2 {
				number := add_port_number[1]
				ovsConfigCmds = append(ovsConfigCmds, []string{"add-port", bridgeName, add_port, "--", "set", "Interface", add_port, fmt.Sprintf("ofport_request=%s", number)})
			} else {
				ovsConfigCmds = append(ovsConfigCmds, []string{"add-port", bridgeName, add_port})
			}
		}
	}

	for _, cmd := range ovsConfigCmds {
		_, err := VsCtl(cmd...)
		if err != nil {
			// At least one bridge config failed, so delete the bridge.
			VsCtl("del-br", bridgeName)
			return err
		}
	}

	// Bring the bridge up
	err := interfaceUp(bridgeName)
	if err != nil {
		log.Warnf("Error enabling bridge: [ %s ]", err)
		VsCtl("del-br", bridgeName)
	}
	return err
}

//  setup bridge, if bridge does not exist create it.
func (d *Driver) initBridge(id string, controller string, dpid string, add_ports string) error {
	bridgeName := d.networks[id].BridgeName
	err := d.ovsdber.createBridge(bridgeName, controller, dpid, add_ports, false)
	if err != nil {
		log.Errorf("Error creating bridge: %s", err)
		return err
	}
	bridgeMode := d.networks[id].Mode
	switch bridgeMode {
	case modeNAT:
		{
			gatewayIP := d.networks[id].Gateway + "/" + d.networks[id].GatewayMask
			if err := setInterfaceIP(bridgeName, gatewayIP); err != nil {
				log.Debugf("Error assigning address: %s on bridge: %s with an error of: %s", gatewayIP, bridgeName, err)
			}

			// Validate that the IPAddress is there!
			_, err := getIfaceAddr(bridgeName)
			if err != nil {
				log.Fatalf("No IP address found on bridge %s", bridgeName)
				return err
			}

			// Add NAT rules for iptables
			if err = natOut(gatewayIP); err != nil {
				log.Fatalf("Could not set NAT rules for bridge %s because %v", bridgeName, err)
				return err
			}
		}

	case modeFlat:
		{
			// NIC is already added to the bridge in createBridge
		}
	}
	return nil
}

// TODO: reconcile with what libnetwork does and port mappings
func natOut(cidr string) error {
	masquerade := []string{
		"POSTROUTING", "-t", "nat",
		"-s", cidr,
		"-j", "MASQUERADE",
	}
	if _, err := iptables.Raw(
		append([]string{"-C"}, masquerade...)...,
	); err != nil {
		incl := append([]string{"-I"}, masquerade...)
		if output, err := iptables.Raw(incl...); err != nil {
			return err
		} else if len(output) > 0 {
			return &iptables.ChainError{
				Chain:  "POSTROUTING",
				Output: output,
			}
		}
	}
	_, err := iptables.Raw("-P", "FORWARD", "ACCEPT")
	return err
}

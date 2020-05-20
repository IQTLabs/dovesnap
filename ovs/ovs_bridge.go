package ovs

import (
	"fmt"
	"strings"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/libnetwork/iptables"
)

// addBridge adds the OVS bridge
func (ovsdber *ovsdber) addBridge(bridgeName string) error {
        return VsCtl("add-br", bridgeName, "--", "set", "Bridge", bridgeName, "stp_enable=false")
}

// deleteBridge deletes the OVS bridge
func (ovsdber *ovsdber) deleteBridge(bridgeName string) error {
        return VsCtl("del-br", bridgeName)
}

//  setupBridge If bridge does not exist create it.
func (d *Driver) initBridge(id string, controller string, dpid string) error {
	bridgeName := d.networks[id].BridgeName
	if err := d.ovsdber.addBridge(bridgeName); err != nil {
		log.Errorf("error creating ovs bridge [ %s ] : [ %s ]", bridgeName, err)
		return err
	}
        var ovsConfigCmds [][]string

	if dpid != "" {
		ovsConfigCmds = append(ovsConfigCmds, []string{"set", "bridge", bridgeName, fmt.Sprintf("other-config:datapath-id=%s", dpid)})
	}

	if controller != "" {
		ovsConfigCmds = append(ovsConfigCmds, []string{"set", "bridge",  bridgeName, "fail-mode=secure"})
		controllers := append([]string{"set-controller", bridgeName}, strings.Split(controller, ",")...)
		ovsConfigCmds = append(ovsConfigCmds, controllers)
	}

	for _, cmd := range ovsConfigCmds {
		err := VsCtl(cmd...)
		if err != nil {
			return err
		}
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
				log.Fatalf("Could not set NAT rules for bridge %s", bridgeName)
				return err
			}
		}

	case modeFlat:
		{
			//ToDo: Add NIC to the bridge
		}
	}

	// Bring the bridge up
	err := interfaceUp(bridgeName)
	if err != nil {
		log.Warnf("Error enabling bridge: [ %s ]", err)
		return err
	}

	return nil
}

// todo: reconcile with what libnetwork does and port mappings
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

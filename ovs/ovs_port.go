package ovs

import (
	"errors"
	"fmt"
)

func (ovsdber *ovsdber) createOvsInternalPort(prefix string, bridge string, tag uint) (port string, err error) {
	// if you desire a longer hash add using generateRandomName(prefix, 5)
	port = prefix
	if ovsdber.ovsdb == nil {
		err = errors.New("OVS not connected")
		return
	}

	ovsdber.addInternalPort(bridge, port, tag)
	return
}

func (ovsdber *ovsdber) addInternalPort(bridgeName string, portName string, tag uint) error {
	if (tag != 0) {
		return VsCtl("add-port", bridgeName, portName, fmt.Sprintf("tag=%u", tag))
	}
	return VsCtl("add-port", bridgeName, portName)
}

func (ovsdber *ovsdber) deletePort(bridgeName string, portName string) error {
	return VsCtl("del-port", bridgeName, portName)
}

func (ovsdber *ovsdber) addVxlanPort(bridgeName string, portName string, peerAddress string) error {
	// http://docs.openvswitch.org/en/latest/faq/vxlan/
	return VsCtl("add-port", bridgeName, portName, "--", "set", "interface", portName, "type=vxlan", fmt.Sprintf("options:remote_ip=%s", peerAddress))
}

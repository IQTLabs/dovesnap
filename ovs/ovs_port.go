package ovs

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func (ovsdber *ovsdber) lowestFreePortOnBridge(bridgeName string) (lowestFreePort uint, err error) {
        output, err := OfCtl("dump-ports-desc", bridgeName)
	if (err != nil) {
		return 0, err
	}
	var ofportNumberDump = regexp.MustCompile(`^\s*(\d+)\(\S+\).+$`)
	existingOfPorts := []int{}
	for _, line := range strings.Split(string(output), "\n") {
		match := ofportNumberDump.FindAllStringSubmatch(line, -1)
		if len(match) > 0 {
			ofport, _ := strconv.Atoi(match[0][1])
			existingOfPorts = append(existingOfPorts, ofport)
		}
        }
	sort.Ints(existingOfPorts)
	intLowestFreePort := 1
	for _, existingPort := range existingOfPorts {
		if existingPort != intLowestFreePort {
			break
		}
		intLowestFreePort++
	}
        return uint(intLowestFreePort), nil
}

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
	lowestFreePort, err := ovsdber.lowestFreePortOnBridge(bridgeName)
	if (err != nil) {
		return err
	}
	if (tag != 0) {
		return VsCtl("add-port", bridgeName, portName, fmt.Sprintf("tag=%u", tag), "--", "set", "Interface", portName, fmt.Sprintf("ofport_request=%d", lowestFreePort))
	}
	return VsCtl("add-port", bridgeName, portName, "--", "set", "Interface", portName, fmt.Sprintf("ofport_request=%d", lowestFreePort))
}

func (ovsdber *ovsdber) deletePort(bridgeName string, portName string) error {
	return VsCtl("del-port", bridgeName, portName)
}

func (ovsdber *ovsdber) addVxlanPort(bridgeName string, portName string, peerAddress string) error {
	// http://docs.openvswitch.org/en/latest/faq/vxlan/
	return VsCtl("add-port", bridgeName, portName, "--", "set", "interface", portName, "type=vxlan", fmt.Sprintf("options:remote_ip=%s", peerAddress))
}

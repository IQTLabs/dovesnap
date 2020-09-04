package ovs

import (
	"fmt"
	"hash/crc32"
	"regexp"
	"sort"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	base62 "kkn.fi/base62"
)

func scrapePortDesc(bridgeName string, portDesc *map[uint]string) error {
	output, err := OfCtl("dump-ports-desc", bridgeName)
	if err != nil {
		return err
	}
	ofportNumberDump := regexp.MustCompile(`^\s*(\d+)\((\S+)\).+$`)
	for _, line := range strings.Split(string(output), "\n") {
		match := ofportNumberDump.FindAllStringSubmatch(line, -1)
		if len(match) > 0 {
			ofport, _ := strconv.Atoi(match[0][1])
			(*portDesc)[uint(ofport)] = match[0][2]
		}
	}
	return nil
}

func (ovsdber *ovsdber) lowestFreePortOnBridge(bridgeName string) (lowestFreePort uint, err error) {
	portDesc := make(map[uint]string)
	err = scrapePortDesc(bridgeName, &portDesc)
	if err != nil {
		return 0, err
	}
	existingOfPorts := []int{}
	for ofport, _ := range portDesc {
		existingOfPorts = append(existingOfPorts, int(ofport))
	}
	sort.Ints(existingOfPorts)
	log.Debugf("existing ports on %s: %+v", bridgeName, existingOfPorts)
	intLowestFreePort := 1
	for _, existingPort := range existingOfPorts {
		if existingPort != intLowestFreePort {
			break
		}
		intLowestFreePort++
	}
	return uint(intLowestFreePort), nil
}

func (ovsdber *ovsdber) addInternalPort(bridgeName string, portName string, tag uint) (uint, string, error) {
	lowestFreePort, err := ovsdber.lowestFreePortOnBridge(bridgeName)
	if err != nil {
		return lowestFreePort, "", err
	}
	if tag != 0 {
		value, err := VsCtl("add-port", bridgeName, portName, fmt.Sprintf("tag=%u", tag), "--", "set", "Interface", portName, fmt.Sprintf("ofport_request=%d", lowestFreePort))
		return lowestFreePort, value, err
	}
	value, err := VsCtl("add-port", bridgeName, portName, "--", "set", "Interface", portName, fmt.Sprintf("ofport_request=%d", lowestFreePort))
	return lowestFreePort, value, err
}

func patchStr(a string) string {
	return base62.Encode(int64(crc32.ChecksumIEEE([]byte(a))))
}

func patchName(a string, b string) string {
	name := patchPrefix + patchStr(a) + patchStr(b)
	if len(name) > 15 {
		panic(fmt.Errorf("%s too long for ifName", name))
	}
	return name
}

func (ovsdber *ovsdber) addPatchPort(bridgeName string, bridgeNamePeer string, port uint, portPeer uint) (uint, uint, error) {
	var err error = nil
	if port == 0 {
		port, err = ovsdber.lowestFreePortOnBridge(bridgeName)
		if err != nil {
			return 0, 0, err
		}
	}
	if portPeer == 0 {
		portPeer, err = ovsdber.lowestFreePortOnBridge(bridgeNamePeer)
		if err != nil {
			return 0, 0, err
		}
	}
	portName := patchName(bridgeName, bridgeNamePeer)
	portNamePeer := patchName(bridgeNamePeer, bridgeName)
	vethPair := netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: portName},
		PeerName:  portNamePeer,
	}
	netlink.LinkAdd(&vethPair)
	netlink.LinkSetUp(&vethPair)
	vethPairPeer := netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: portNamePeer},
		PeerName:  portName,
	}
	netlink.LinkSetUp(&vethPairPeer)
	_, err = VsCtl("add-port", bridgeName, portName, "--", "set", "Interface", portName, fmt.Sprintf("ofport_request=%d", port))
	_, err = VsCtl("add-port", bridgeNamePeer, portNamePeer, "--", "set", "Interface", portNamePeer, fmt.Sprintf("ofport_request=%d", portPeer))
	//_, err = VsCtl("set", "interface", portName, "type=patch")
	//_, err = VsCtl("set", "interface", portNamePeer, "type=patch")
	//_, err = VsCtl("set", "interface", portName, fmt.Sprintf("options:peer=%s", portNamePeer))
	//_, err = VsCtl("set", "interface", portNamePeer, fmt.Sprintf("options:peer=%s", portName))
	return port, portPeer, err
}

func (ovsdber *ovsdber) deletePatchPort(bridgeName string, bridgeNamePeer string) error {
	portName := patchName(bridgeName, bridgeNamePeer)
	portNamePeer := patchName(bridgeNamePeer, bridgeName)
	_, err := VsCtl("del-port", bridgeName, portName)
	_, err = VsCtl("del-port", bridgeNamePeer, portNamePeer)
	vethPair := netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: portName},
		PeerName:  portNamePeer,
	}
	netlink.LinkDel(&vethPair)
	return err
}

func (ovsdber *ovsdber) deletePort(bridgeName string, portName string) error {
	_, err := VsCtl("del-port", bridgeName, portName)
	return err
}

func (ovsdber *ovsdber) getOfPortNumber(portName string) (uint, error) {
	ofport, err := VsCtl("get", "Interface", portName, "ofport")
	if err != nil {
		log.Errorf("Unable to get interface %s ofport number because: %v", portName, err)
		return 0, err
	}
	ofportNum, err := strconv.ParseUint(ofport, 10, 32)
	if err != nil {
		log.Errorf("Unable to convert ofport number %v to an unsigned integer because: %v", ofport, err)
		return 0, err
	}
	return uint(ofportNum), nil
}

func (ovsdber *ovsdber) addVxlanPort(bridgeName string, portName string, peerAddress string) (string, error) {
	// http://docs.openvswitch.org/en/latest/faq/vxlan/
	value, err := VsCtl("add-port", bridgeName, portName, "--", "set", "interface", portName, "type=vxlan", fmt.Sprintf("options:remote_ip=%s", peerAddress))
	return value, err
}

package ovs

import (
	"fmt"
	"os/exec"
	"strings"

	log "github.com/sirupsen/logrus"
)

const (
	ovsofctlPath   = "/usr/bin/ovs-ofctl"
	ovsvsctlPath   = "/usr/bin/ovs-vsctl"
	ovsvsctlDBPath = "unix:/var/run/openvswitch/db.sock"
)

func VsCtl(args ...string) (string, error) {
	all := append([]string{fmt.Sprintf("--db=%s", ovsvsctlDBPath)}, args...)
	output, err := exec.Command(ovsvsctlPath, all...).CombinedOutput()
	if err != nil {
		log.Debugf("FAILED: %s, %v, %s", ovsvsctlPath, all, output)
	} else {
		log.Debugf("OK: %s, %v", ovsvsctlPath, all)
	}
	return strings.TrimSuffix(string(output), "\n"), err
}

func mustVsCtl(args ...string) string {
	output, err := VsCtl(args...)
	if err != nil {
		panic(err)
	}
	return output
}

func OfCtl(args ...string) ([]byte, error) {
	output, err := exec.Command(ovsofctlPath, args...).CombinedOutput()
	if err != nil {
		log.Debugf("FAILED: %s, %v, %s", ovsofctlPath, args, output)
	} else {
		log.Debugf("OK: %s, %v", ovsofctlPath, args)
	}
	return output, err
}

func mustOfCtl(args ...string) []byte {
	output, err := OfCtl(args...)
	if err != nil {
		panic(err)
	}
	return output
}

type ovsdber struct {
}

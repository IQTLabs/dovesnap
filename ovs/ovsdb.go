package ovs

import (
	"fmt"
	"os/exec"

	log "github.com/Sirupsen/logrus"
)

const (
	ovsofctlPath   = "/usr/local/bin/ovs-ofctl"
	ovsvsctlPath   = "/usr/local/bin/ovs-vsctl"
	ovsvsctlDBPath = "unix:/usr/local/var/run/openvswitch/db.sock"
)

func VsCtl(args ...string) error {
	all := append([]string{fmt.Sprintf("--db=%s", ovsvsctlDBPath)}, args...)
	output, err := exec.Command(ovsvsctlPath, all...).CombinedOutput()
	if err != nil {
		log.Debugf("FAILED: %s, %v, %s", ovsvsctlPath, all, output)
	} else {
		log.Debugf("OK: %s, %v", ovsvsctlPath, all)
	}
	return err
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

type ovsdber struct {
}

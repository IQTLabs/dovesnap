package ovs

import (
	"fmt"
	"os/exec"

	log "github.com/Sirupsen/logrus"
)

const (
	localhost    = "127.0.0.1"
	ovsdbPort    = 6640
)

func VsCtl(args ...string) (error) {
	ovsvsctlPath := "/usr/local/bin/ovs-vsctl"
	// TODO: we can use the Unix socket instead.
	dbStr := fmt.Sprintf("--db=tcp:%s:%d", localhost, ovsdbPort)
	all := append([]string{dbStr}, args...)
	output, err := exec.Command(ovsvsctlPath, all...).CombinedOutput()
	if err != nil {
		log.Debugf("FAILED: %s, %v, %s", ovsvsctlPath, all, output)
	} else {
		log.Debugf("OK: %s, %v", ovsvsctlPath, all)
	}
	return err
}

func OfCtl(args ...string) ([]byte, error) {
	ovsofctlPath := "/usr/local/bin/ovs-ofctl"
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

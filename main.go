package main

import (
	"flag"

	ovs "dovesnap/ovs"
	"github.com/docker/go-plugins-helpers/network"
	log "github.com/sirupsen/logrus"
)

const (
	version = "0.5.0"
)

func main() {
	flagDebug := flag.Bool("debug", false, "enable debugging")
	flagFaucetconfrpcServerName := flag.String(
		"faucetconfrpc_addr", "localhost", "address of faucetconfrpc server")
	flagFaucetconfrpcServerPort := flag.Int(
		"faucetconfrpc_port", 59999, "port for faucetconfrpc server")
	flagFaucetconfrpcKeydir := flag.String(
		"faucetconfrpc_keydir", "/faucetconfrpc", "directory with keys for faucetconfrpc server")
	flagStackingInterfaces := flag.String(
		"stacking_interfaces", "", "comma separated list of [dpname:port:interface_name] to use for stacking")
	flagStackPriority1 := flag.String(
		"stack_priority1", "", "dp name of switch to give stacking priority 1")
	flagStackMirrorInterface := flag.String(
		"stack_mirror_interface", "", "stack tunnel mirroring configuration [mirrordpname:mirrorport]")
	flagDefaultControllers := flag.String(
		"default_ofcontrollers", "", "default OF controllers to use (must be defined if stacking is used)")
	flagMirrorBridgeIn := flag.String(
		"mirror_bridge_in", "", "optional input interface from another mirror bridge")
	flagMirrorBridgeOut := flag.String(
		"mirror_bridge_out", "", "output interface from mirror bridge")
	flag.Parse()
	if *flagDebug {
		log.SetLevel(log.DebugLevel)
	}
	d := ovs.NewDriver(
		*flagFaucetconfrpcServerName,
		*flagFaucetconfrpcServerPort,
		*flagFaucetconfrpcKeydir,
		*flagStackPriority1,
		*flagStackingInterfaces,
		*flagStackMirrorInterface,
		*flagDefaultControllers,
		*flagMirrorBridgeIn,
		*flagMirrorBridgeOut)
	log.Infof("New Docker driver created")
	h := network.NewHandler(d)
	log.Infof("Getting ready to serve new Docker driver")
	h.ServeUnix(ovs.DriverName, 0)
}

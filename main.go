package main

import (
	"flag"

	ovs "dovesnap/ovs"
	"github.com/docker/go-plugins-helpers/network"
	log "github.com/sirupsen/logrus"
)

const (
	version = "0.1.1"
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
	flag.Parse()
	if *flagDebug {
		log.SetLevel(log.DebugLevel)
	}
	d, err := ovs.NewDriver(
		*flagFaucetconfrpcServerName,
		*flagFaucetconfrpcServerPort,
		*flagFaucetconfrpcKeydir,
		*flagStackPriority1,
		*flagStackingInterfaces,
		*flagStackMirrorInterface,
		*flagDefaultControllers)
	if err != nil {
		panic(err)
	}
	h := network.NewHandler(d)
	h.ServeUnix(ovs.DriverName, 0)
}

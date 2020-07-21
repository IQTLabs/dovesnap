package main

import (
	"flag"

	ovs "dovesnap/ovs"
	log "github.com/Sirupsen/logrus"
	"github.com/docker/go-plugins-helpers/network"
)

const (
	version = "0.1.1"
)

func main() {
	flagDebug := flag.Bool("debug", false, "enable debugging")
	flagFaucetconfrpcServerName := flag.String("faucetconfrpc_addr", "localhost", "address of faucetconfrpc server")
	flagFaucetconfrpcServerPort := flag.Int("faucetconfrpc_port", 59999, "port for faucetconfrpc server")
	flagFaucetconfrpcKeydir := flag.String("faucetconfrpc_keydir", "/faucetconfrpc", "directory with keys for faucetconfrpc server")
	flagStackingInterfaces := flag.String("stacking_interfaces", "", "comma separated list of [dpid:port:interface_name] to use for stacking")
	flag.Parse()
	if *flagDebug {
		log.SetLevel(log.DebugLevel)
	}
	d, err := ovs.NewDriver(
		*flagFaucetconfrpcServerName,
		*flagFaucetconfrpcServerPort,
		*flagFaucetconfrpcKeydir,
		*flagStackingInterfaces)
	if err != nil {
		panic(err)
	}
	h := network.NewHandler(d)
	h.ServeUnix(ovs.DriverName, 0)
}

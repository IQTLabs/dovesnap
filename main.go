package main

import (
	"os"

	log "github.com/Sirupsen/logrus"
	"github.com/codegangsta/cli"
	"github.com/docker/go-plugins-helpers/network"
	ovs "dovesnap/ovs"
)

const (
	version = "0.1.1"
)

func main() {
	flagDebug := cli.BoolFlag{
		Name:  "debug, d",
		Usage: "enable debugging",
	}
	flagFaucetconfrpcServerName := cli.StringFlag{
		Name: "faucetconfrpc_addr",
		Usage: "address of faucetconfrpc server",
		Value: "localhost",
	}
	flagFaucetconfrpcServerPort := cli.IntFlag{
		Name: "faucetconfrpc_port",
		Usage: "port for faucetconfrpc server",
		Value: 59999,
        }
	flagFaucetconfrpcKeydir := cli.StringFlag{
		Name: "faucetconfrpc_keydir",
		Usage: "directory with keys for faucetconfrpc server",
		Value: "/faucetconfrpc",
        }
	app := cli.NewApp()
	app.Name = "dovesnap"
	app.Usage = "Docker Open vSwitch Network Plugin"
	app.Version = version
	app.Flags = []cli.Flag{
		flagDebug,
		flagFaucetconfrpcServerName,
		flagFaucetconfrpcServerPort,
		flagFaucetconfrpcKeydir,
	}
	app.Action = Run
	app.Run(os.Args)
}

// Run initializes the driver
func Run(ctx *cli.Context) {
	if ctx.Bool("debug") {
		log.SetLevel(log.DebugLevel)
	}
	d, err := ovs.NewDriver(
		ctx.String("faucetconfrpc_addr"),
		ctx.Int("faucetconfrpc_port"),
		ctx.String("faucetconfrpc_keydir"))
	if err != nil {
		panic(err)
	}
	h := network.NewHandler(d)
	h.ServeUnix(ovs.DriverName, 0)
}

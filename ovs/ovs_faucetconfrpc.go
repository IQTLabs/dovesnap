package ovs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"strconv"
	"time"

	"github.com/iqtlabs/faucetconfrpc/faucetconfrpc"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type faucetconfrpcer struct {
	client faucetconfserver.FaucetConfServerClient
}

func (c *faucetconfrpcer) mustGetGRPCClient(flagFaucetconfrpcClientName string, flagFaucetconfrpcServerName string, flagFaucetconfrpcServerPort int, flagFaucetconfrpcKeydir string) {
	crt_file := fmt.Sprintf("%s/%s.crt", flagFaucetconfrpcKeydir, flagFaucetconfrpcClientName)
	key_file := fmt.Sprintf("%s/%s.key", flagFaucetconfrpcKeydir, flagFaucetconfrpcClientName)
	ca_file := flagFaucetconfrpcKeydir + "/" + flagFaucetconfrpcServerName + "-ca.crt"
	certificate, err := tls.LoadX509KeyPair(crt_file, key_file)
	if err != nil {
		panic(err)
	}
	log.Debugf("Certificates loaded")
	certPool := x509.NewCertPool()
	ca, err := ioutil.ReadFile(ca_file)
	if err != nil {
		panic(err)
	}
	if err := certPool.AppendCertsFromPEM(ca); !err {
		panic(err)
	}
	creds := credentials.NewTLS(&tls.Config{
		ServerName:   flagFaucetconfrpcServerName,
		Certificates: []tls.Certificate{certificate},
		RootCAs:      certPool,
	})

	// Connect to faucetconfrpc server.
	addr := flagFaucetconfrpcServerName + ":" + strconv.Itoa(flagFaucetconfrpcServerPort)
	log.Debugf("Connecting to RPC server: %v", addr)
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(creds), grpc.WithBlock(), grpc.WithTimeout(30*time.Second))
	if err != nil {
		panic(err)
	}
	log.Debugf("Connected to RPC server")
	c.client = faucetconfserver.NewFaucetConfServerClient(conn)
}

func (c *faucetconfrpcer) mustSetFaucetConfigFile(config_yaml string) {
	log.Debugf("setFaucetConfigFile %s", config_yaml)
	req := &faucetconfserver.SetConfigFileRequest{
		ConfigYaml: config_yaml,
		Merge:      true,
	}
	_, err := c.client.SetConfigFile(context.Background(), req)
	if err != nil {
		panic(err)
	}
}

func (c *faucetconfrpcer) mustDeleteDpInterface(dpName string, ofport uint32) {
	interfaces := &faucetconfserver.InterfaceInfo{
		PortNo: ofport,
	}
	interfacesConf := []*faucetconfserver.DpInfo{
		{
			Name:       dpName,
			Interfaces: []*faucetconfserver.InterfaceInfo{interfaces},
		},
	}

	req := &faucetconfserver.DelDpInterfacesRequest{
		InterfacesConfig: interfacesConf,
		DeleteEmptyDp:    true,
	}

	_, err := c.client.DelDpInterfaces(context.Background(), req)
	if err != nil {
		panic(err)
	}
}

func (c *faucetconfrpcer) mustDeleteDp(dpName string) {
	dp := []*faucetconfserver.DpInfo{
		{
			Name: dpName,
		},
	}
	dReq := &faucetconfserver.DelDpsRequest{
		InterfacesConfig: dp,
	}

	_, err := c.client.DelDps(context.Background(), dReq)
	if err != nil {
		panic(err)
	}
}

func (c *faucetconfrpcer) mustAddPortMirror(dpName string, ofport uint32, mirrorofport uint32) {
	req := &faucetconfserver.AddPortMirrorRequest{
		DpName:       dpName,
		PortNo:       ofport,
		MirrorPortNo: mirrorofport,
	}
	_, err := c.client.AddPortMirror(context.Background(), req)
	if err != nil {
		panic(err)
	}
}

func (c *faucetconfrpcer) mustSetRemoteMirrorPort(dpName string, ofport uint32, vid uint32, remoteDpName string, remoteofport uint32) {
	req := &faucetconfserver.SetRemoteMirrorPortRequest{
		DpName:       dpName,
		PortNo:       ofport,
		TunnelVid:    vid,
		RemoteDpName: remoteDpName,
		RemotePortNo: remoteofport,
	}
	_, err := c.client.SetRemoteMirrorPort(context.Background(), req)
	if err != nil {
		panic(err)
	}
}

func (c *faucetconfrpcer) vlanInterfaceYaml(ofport uint32, description string, vlan uint, acls_in string) string {
	return fmt.Sprintf("%d: {description: %s, native_vlan: %d, acls_in: [%s]},", ofport, description, vlan, acls_in)
}

func (c *faucetconfrpcer) stackInterfaceYaml(ofport uint32, remoteDpName string, remoteOfport uint32) string {
	return fmt.Sprintf("%d: {description: stack link to %s, stack: {dp: %s, port: %d}},", ofport, remoteDpName, remoteDpName, remoteOfport)
}

func (c *faucetconfrpcer) mergeInterfacesYaml(dpName string, uintDpid uint64, description string, addInterfaces string) string {
	return fmt.Sprintf("{dps: {%s: {dp_id: %d, description: OVS Bridge %s, interfaces: {%s}}}}",
		dpName, uintDpid, description, addInterfaces)
}

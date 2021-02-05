package ovs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	log "github.com/sirupsen/logrus"
)

type dockerer struct {
	client *client.Client
}

func (c *dockerer) mustGetDockerClient() {
	// docker, err := client.NewClientWithOpts(client.FromEnv)
	// TODO: https://github.com/moby/moby/issues/40185
	client, err := client.NewEnvClient()
	if err != nil {
		panic(fmt.Errorf("Could not connect to docker: %s", err))
	}
	c.client = client
}

func (c *dockerer) mustGetShortEngineID() string {
	info, err := c.client.Info(context.Background())
	if err != nil {
		panic(err)
	}
	log.Debugf("Docker Engine ID %s:", info.ID)
	engineId := base36to16(strings.Split(info.ID, ":")[0])
	return engineId
}

func (c *dockerer) mustGetNetworkInspectFromID(NetworkID string) types.NetworkResource {
	for i := 0; i < dockerRetries; i++ {
		netInspect, err := c.client.NetworkInspect(context.Background(), NetworkID, types.NetworkInspectOptions{})
		if err == nil {
			return netInspect
		}
		time.Sleep(1 * time.Second)
	}
	panic(fmt.Errorf("Network %s not found", NetworkID))
}

func (c *dockerer) mustGetNetworkNameFromID(NetworkID string) string {
	return c.mustGetNetworkInspectFromID(NetworkID).Name
}

func (c *dockerer) mustGetNetworkList() map[string]string {
	networkList, err := c.client.NetworkList(context.Background(), types.NetworkListOptions{})
	if err != nil {
		panic(fmt.Errorf("Could not get docker networks: %s", err))
	}
	netlist := make(map[string]string)
	for _, net := range networkList {
		if net.Driver == DriverName {
			netlist[net.ID] = net.Name
		}
	}
	return netlist
}

func (c *dockerer) getContainerFromEndpoint(EndpointID string) (types.ContainerJSON, error) {
	netlist := c.mustGetNetworkList()
	for i := 0; i < dockerRetries; i++ {
		for id, _ := range netlist {
			netInspect := c.mustGetNetworkInspectFromID(id)
			for containerID, containerInfo := range netInspect.Containers {
				if containerInfo.EndpointID == EndpointID {
					containerInspect, err := c.client.ContainerInspect(context.Background(), containerID)
					if err != nil {
						continue
					}
					return containerInspect, nil
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
	return types.ContainerJSON{}, fmt.Errorf("Endpoint %s not found", EndpointID)
}

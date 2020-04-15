package ovs

// this needs to be migrated to a maintained project
import "github.com/samalba/dockerclient"

type dockerer struct {
	client *dockerclient.DockerClient
}

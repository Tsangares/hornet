package framework

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// newDockerClient creates a Docker client that communicates via the Docker socket.
func newDockerClient() (*client.Client, error) {
	return client.NewClient(
		"unix:///var/run/docker.sock",
		"",
		nil,
		nil,
	)
}

// DockerContainer is a wrapper object for a Docker container.
type DockerContainer struct {
	client *client.Client
	id     string
}

// NewDockerContainer creates a new DockerContainer.
func NewDockerContainer(c *client.Client) *DockerContainer {
	return &DockerContainer{client: c}
}

// NewDockerContainerFromExisting creates a new DockerContainer from an already existing Docker container by name.
func NewDockerContainerFromExisting(c *client.Client, name string) (*DockerContainer, error) {
	containers, err := c.ContainerList(context.Background(), types.ContainerListOptions{})
	if err != nil {
		return nil, err
	}

	for _, cont := range containers {
		if cont.Names[0] == name {
			return &DockerContainer{
				client: c,
				id:     cont.ID,
			}, nil
		}
	}

	return nil, fmt.Errorf("could not find container with name '%s'", name)
}

// CreateHornetEntryNode creates a new container with the Hornet entry node's configuration.
func (d *DockerContainer) CreateHornetEntryNode(name string, seed string) error {
	containerConfig := &container.Config{
		Image:        "hornet:dev",
		ExposedPorts: nil,
		Cmd: strslice.StrSlice{
			"--logger.level=debug",
			fmt.Sprintf("--node.disablePlugins=%s", disabledPluginsEntryNode),
			"--autopeering.entryNodes=",
			fmt.Sprintf("--autopeering.seed=base58:%s", seed),
		},
	}

	return d.CreateContainer(name, containerConfig)
}

// CreateHornetPeer creates a new container with the Hornet peer's configuration.
func (d *DockerContainer) CreateHornetPeer(config NodeConfig) error {
	// configure Hornet container instance
	containerConfig := &container.Config{
		Image: "hornet:dev",
		ExposedPorts: nat.PortSet{
			nat.Port(fmt.Sprintf("%d/tcp", APIPort)): {},
		},
		Env: []string{fmt.Sprintf("COO_SEED=%s", coordinatorSeed)},
		Cmd: strslice.StrSlice{
			"--logger.level=debug",
			fmt.Sprintf("--node.disablePlugins=%s", config.DisabledPlugins),
			fmt.Sprintf("--node.enablePlugins=%s", func() string {
				var plugins []string
				if config.Coordinator {
					plugins = append(plugins, "Coordinator")
				}
				return strings.Join(plugins, ",")
			}()),
			fmt.Sprintf("--coordinator.mwm=%d", ParaPoWDifficulty),
			fmt.Sprintf("--coordinator.address=%s", coordinatorAddress),
			fmt.Sprintf("--coordinator.intervalSeconds=%d", coordinatorIntervalSeconds),
			fmt.Sprintf("--coordinator.securityLevel=%d", coordinatorSecurityLevel),
			fmt.Sprintf("--coordinator.merkleTreeDepth=%d", coordinatorMerkleTreeDepth),
			"--snapshots.loadType=global",
			"--snapshots.global.path=snapshot.csv",
			"--snapshots.global.index=0",
			fmt.Sprintf("--snapshots.local.path=%s", config.SnapshotFilePath),
			fmt.Sprintf("--httpAPI.bindAddress=%d", APIPort),
			fmt.Sprintf("--autopeering.seed=base58:%s", config.AutopeeringSeed),
			fmt.Sprintf("--autopeering.entryNodes=%s@%s:14626", config.EntryNodePublicKey, config.EntryNodeHost),
		},
	}

	return d.CreateContainer(config.Name, containerConfig, &container.HostConfig{
		Binds: []string{"hornet-testing-assets:/assets:rw"},
	})
}

// CreatePumba creates a new container with Pumba configuration.
func (d *DockerContainer) CreatePumba(name string, containerName string, targetIPs []string) error {
	hostConfig := &container.HostConfig{
		Binds: strslice.StrSlice{"/var/run/docker.sock:/var/run/docker.sock:ro"},
	}

	cmd := strslice.StrSlice{
		"--log-level=debug",
		"netem",
		"--duration=100m",
	}

	for _, ip := range targetIPs {
		targetFlag := "--target=" + ip
		cmd = append(cmd, targetFlag)
	}

	slice := strslice.StrSlice{
		"--tc-image=gaiadocker/iproute2",
		"loss",
		"--percent=100",
		containerName,
	}
	cmd = append(cmd, slice...)

	containerConfig := &container.Config{
		Image: "gaiaadm/pumba:0.7.2",
		Cmd:   cmd,
	}

	return d.CreateContainer(name, containerConfig, hostConfig)
}

// CreateContainer creates a new container with the given configuration.
func (d *DockerContainer) CreateContainer(name string, containerConfig *container.Config, hostConfigs ...*container.HostConfig) error {
	var hostConfig *container.HostConfig
	if len(hostConfigs) > 0 {
		hostConfig = hostConfigs[0]
	}

	resp, err := d.client.ContainerCreate(context.Background(), containerConfig, hostConfig, nil, name)
	if err != nil {
		return err
	}

	d.id = resp.ID
	return nil
}

// ConnectToNetwork connects a container to an existent network in the docker host.
func (d *DockerContainer) ConnectToNetwork(networkID string) error {
	return d.client.NetworkConnect(context.Background(), networkID, d.id, nil)
}

// DisconnectFromNetwork disconnects a container from an existent network in the docker host.
func (d *DockerContainer) DisconnectFromNetwork(networkID string) error {
	return d.client.NetworkDisconnect(context.Background(), networkID, d.id, true)
}

// Start sends a request to the docker daemon to start a container.
func (d *DockerContainer) Start() error {
	return d.client.ContainerStart(context.Background(), d.id, types.ContainerStartOptions{})
}

// Remove kills and removes a container from the docker host.
func (d *DockerContainer) Remove() error {
	return d.client.ContainerRemove(context.Background(), d.id, types.ContainerRemoveOptions{Force: true})
}

// Stop stops a container without terminating the process.
// The process is blocked until the container stops or the timeout expires.
func (d *DockerContainer) Stop(optionalTimeout ...time.Duration) error {
	duration := 3 * time.Minute
	if optionalTimeout != nil {
		duration = optionalTimeout[0]
	}
	return d.client.ContainerStop(context.Background(), d.id, &duration)
}

// ExitStatus returns the exit status according to the container information.
func (d *DockerContainer) ExitStatus() (int, error) {
	resp, err := d.client.ContainerInspect(context.Background(), d.id)
	if err != nil {
		return -1, err
	}

	return resp.State.ExitCode, nil
}

// IP returns the IP address according to the container information for the given network.
func (d *DockerContainer) IP(network string) (string, error) {
	resp, err := d.client.ContainerInspect(context.Background(), d.id)
	if err != nil {
		return "", err
	}

	for name, v := range resp.NetworkSettings.Networks {
		if name == network {
			return v.IPAddress, nil
		}
	}

	return "", fmt.Errorf("IP address in %s could not be determined", network)
}

// Logs returns the logs of the container as io.ReadCloser.
func (d *DockerContainer) Logs() (io.ReadCloser, error) {
	options := types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Since:      "",
		Timestamps: false,
		Follow:     false,
		Tail:       "",
		Details:    false,
	}

	return d.client.ContainerLogs(context.Background(), d.id, options)
}
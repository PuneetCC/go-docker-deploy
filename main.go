package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	cliconfig "github.com/docker/cli/cli/config"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/docker/go-connections/nat"

	"github.com/moby/term"

	"github.com/docker/docker/client"
	"github.com/gofiber/fiber/v2"
)

type DockerPortBindingRequest struct {
	Container string `json:"container"`
	Host      string `json:"host"`
}

type DockerVolumeBindingRequest struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type DockerRequest struct {
	ContainerName  string                       `json:"name"`
	CustomCommand  []string                     `json:"customCommand"`
	Image          string                       `json:"image"`
	PortBindings   []DockerPortBindingRequest   `json:"portBindings"`
	VolumeBindings []DockerVolumeBindingRequest `json:"volumeBindings"`
	Environment    []string                     `json:"environment"`
	Memory         string                       `json:"memory"`
	CPUShares      string                       `json:"cpuShares"`
}

var privateDockerRegistry = "d.puneet.cc"
var docker *client.Client

func convertToBytes(val string) (int64, error) {
	units := map[string]int64{
		"KB": 1024,
		"MB": 1024 * 1024,
		"GB": 1024 * 1024 * 1024,
		"TB": 1024 * 1024 * 1024 * 1024,
	}
	val = strings.ToUpper(val)
	unit := val[len(val)-2:]
	if _, ok := units[unit]; !ok {
		unit = val[len(val)-1:]
		if _, ok := units[unit]; !ok {
			return 0, fmt.Errorf("Invalid unit: %s", unit)
		}
		val = val[:len(val)-1]
	}
	numStr := val[:len(val)-len(unit)]
	num, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 0, err
	}
	return num * units[unit], nil
}

func startContainer(request DockerRequest) error {
	if !strings.HasPrefix(request.Image, privateDockerRegistry) {
		return errors.New("only " + privateDockerRegistry + " images supported")
	}

	// Load docker registry config
	cfg, err := cliconfig.Load("")
	if err != nil {
		return errors.New("config load failed")
	}

	conf, _ := cfg.GetAuthConfig(privateDockerRegistry)
	registryAuthConfig := types.AuthConfig(conf)
	jsonRegistryAuth, _ := json.Marshal(registryAuthConfig)
	registryAuthBase64 := base64.StdEncoding.EncodeToString([]byte(jsonRegistryAuth))

	reader, err := docker.ImagePull(context.Background(), request.Image, types.ImagePullOptions{
		RegistryAuth: registryAuthBase64,
	})

	if err != nil {
		return err
	}
	defer reader.Close()

	termFd, isTerm := term.GetFdInfo(os.Stderr)
	jsonmessage.DisplayJSONMessagesStream(reader, os.Stderr, termFd, isTerm, nil)

	_, err = docker.ContainerInspect(context.Background(), request.ContainerName)
	if err == nil {
		// container exists - stop and remove
		err = docker.ContainerStop(context.Background(), request.ContainerName, nil)
		if err != nil {
			fmt.Println("[startContainer][ContainerStop][ERROR] : " + err.Error())
		}
		err = docker.ContainerRemove(context.Background(), request.ContainerName, types.ContainerRemoveOptions{})
		if err != nil {
			fmt.Println("[startContainer][ContainerRemove][ERROR] : " + err.Error())
		}
	}

	hostConfig := &container.HostConfig{}

	hostConfig.RestartPolicy = container.RestartPolicy{
		Name: "always",
	}

	// handle memory and cpushare allocation
	if request.Memory != "" || request.CPUShares != "" {
		hostConfig.Resources = container.Resources{}
	}

	if request.Memory != "" {
		memoryInBytes, err := convertToBytes(request.Memory)
		if err != nil {
			fmt.Println("[startContainer][Memory-Parsing][ERROR] : " + err.Error())
			return err
		}
		hostConfig.Resources.Memory = memoryInBytes
	}

	if request.CPUShares != "" {
		cpuShares, err := strconv.ParseInt(request.CPUShares, 10, 64)
		if err != nil {
			fmt.Println("[startContainer][CPUShares-Parsing][ERROR] : " + err.Error())
			return err
		}
		hostConfig.Resources.CPUShares = cpuShares
	}

	// add: Volume Bindings
	if len(request.VolumeBindings) > 0 {
		hostConfig.Mounts = []mount.Mount{}
		for _, volume := range request.VolumeBindings {
			hostConfig.Mounts = append(hostConfig.Mounts, mount.Mount{
				Type:   mount.TypeBind,
				Source: volume.Source,
				Target: volume.Target,
			})
		}
	}

	// add: Port Bindings
	if len(request.PortBindings) > 0 {
		hostConfig.PortBindings = map[nat.Port][]nat.PortBinding{}
		for _, port := range request.PortBindings {
			hostConfig.PortBindings[nat.Port(port.Container)] = []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: port.Host},
			}
		}
	}

	containerConfig := &container.Config{Image: request.Image}

	if len(request.CustomCommand) > 0 {
		containerConfig.Cmd = request.CustomCommand
	}

	if len(request.Environment) > 0 {
		containerConfig.Env = request.Environment
	}

	// add: Port Bindings
	if len(request.PortBindings) > 0 {
		containerConfig.ExposedPorts = nat.PortSet{}
		for _, port := range request.PortBindings {
			containerConfig.ExposedPorts[nat.Port(port.Container)] = struct{}{}
		}
	}

	c, err := docker.ContainerCreate(
		context.Background(),
		containerConfig,
		hostConfig,
		nil, nil,
		request.ContainerName,
	)
	if err != nil {
		fmt.Println("[startContainer][ContainerCreate][ERROR] : " + err.Error())
		return err
	}
	err = docker.ContainerStart(context.Background(), c.ID, types.ContainerStartOptions{})
	return err
}

func main() {
	dockerCli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		panic(err)
	}
	docker = dockerCli

	app := fiber.New()

	app.Post("/", func(c *fiber.Ctx) error {
		// if c.Get("x-api-key") != os.Getenv("DOCKER_DEPLOY_SECRET") {
		// 	return c.Status(500).JSON(&fiber.Map{"error": 1, "message": "Unauthorised Access"})
		// }
		var request DockerRequest
		if err := c.BodyParser(&request); err != nil {
			return c.Status(500).JSON(&fiber.Map{"error": 1, "message": err.Error()})
		}
		err := startContainer(request)
		if err != nil {
			return c.Status(500).JSON(&fiber.Map{"error": 1, "message": err.Error()})
		}
		return c.JSON(&fiber.Map{"error": 0, "message": "Container Started"})
	})

	app.Listen(":4444")
}

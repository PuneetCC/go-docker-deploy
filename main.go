package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

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
	Image          string                       `json:"image"`
	PortBindings   []DockerPortBindingRequest   `json:"portBindings"`
	VolumeBindings []DockerVolumeBindingRequest `json:"volumeBindings"`
	Environment    []string                     `json:"environment"`
}

var docker *client.Client

func startContainer(request DockerRequest) error {
	if !strings.HasPrefix(request.Image, "d.puneet.cc") {
		return errors.New("only d.puneet.cc images supported")
	}

	reader, err := docker.ImagePull(context.Background(), request.Image, types.ImagePullOptions{})

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
		if c.Get("x-api-key") != os.Getenv("DOCKER_DEPLOY_SECRET") {
			return c.Status(500).JSON(&fiber.Map{"error": 1, "message": "Unauthorised Access"})
		}
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

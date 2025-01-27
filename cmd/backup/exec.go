// Copyright 2022 - Offen Authors <hioffen@posteo.de>
// SPDX-License-Identifier: MPL-2.0

// Portions of this file are taken and adapted from `moby`, Copyright 2012-2017 Docker, Inc.
// Licensed under the Apache 2.0 License: https://github.com/moby/moby/blob/8e610b2b55bfd1bfa9436ab110d311f5e8a74dcb/LICENSE

package main

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	"github.com/cosiner/argv"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/pkg/stdcopy"
	"golang.org/x/sync/errgroup"
)

func (s *script) exec(containerRef string, command string) ([]byte, []byte, error) {
	args, _ := argv.Argv(command, nil, nil)
	execID, err := s.cli.ContainerExecCreate(context.Background(), containerRef, types.ExecConfig{
		Cmd:          args[0],
		AttachStdin:  true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("exec: error creating container exec: %w", err)
	}

	resp, err := s.cli.ContainerExecAttach(context.Background(), execID.ID, types.ExecStartCheck{})
	if err != nil {
		return nil, nil, fmt.Errorf("exec: error attaching container exec: %w", err)
	}
	defer resp.Close()

	var outBuf, errBuf bytes.Buffer
	outputDone := make(chan error)

	go func() {
		_, err := stdcopy.StdCopy(&outBuf, &errBuf, resp.Reader)
		outputDone <- err
	}()

	select {
	case err := <-outputDone:
		if err != nil {
			return nil, nil, fmt.Errorf("exec: error demultiplexing output: %w", err)
		}
		break
	}

	stdout, err := ioutil.ReadAll(&outBuf)
	if err != nil {
		return nil, nil, fmt.Errorf("exec: error reading stdout: %w", err)
	}
	stderr, err := ioutil.ReadAll(&errBuf)
	if err != nil {
		return nil, nil, fmt.Errorf("exec: error reading stderr: %w", err)
	}

	res, err := s.cli.ContainerExecInspect(context.Background(), execID.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("exec: error inspecting container exec: %w", err)
	}

	if res.ExitCode > 0 {
		return stdout, stderr, fmt.Errorf("exec: running command exited %d", res.ExitCode)
	}

	return stdout, stderr, nil
}

func (s *script) runLabeledCommands(label string) error {
	f := []filters.KeyValuePair{
		{Key: "label", Value: label},
	}
	if s.c.ExecLabel != "" {
		f = append(f, filters.KeyValuePair{
			Key:   "label",
			Value: fmt.Sprintf("docker-volume-backup.exec-label=%s", s.c.ExecLabel),
		})
	}
	containersWithCommand, err := s.cli.ContainerList(context.Background(), types.ContainerListOptions{
		Quiet:   true,
		Filters: filters.NewArgs(f...),
	})
	if err != nil {
		return fmt.Errorf("runLabeledCommands: error querying for containers: %w", err)
	}

	if len(containersWithCommand) == 0 {
		return nil
	}

	g := new(errgroup.Group)

	for _, container := range containersWithCommand {
		c := container
		g.Go(func() error {
			cmd, _ := c.Labels[label]
			s.logger.Infof("Running %s command %s for container %s", label, cmd, strings.TrimPrefix(c.Names[0], "/"))
			stdout, stderr, err := s.exec(c.ID, cmd)
			if s.c.ExecForwardOutput {
				os.Stderr.Write(stderr)
				os.Stdout.Write(stdout)
			}
			if err != nil {
				return fmt.Errorf("runLabeledCommands: error executing command: %w", err)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return fmt.Errorf("runLabeledCommands: error from errgroup: %w", err)
	}
	return nil
}

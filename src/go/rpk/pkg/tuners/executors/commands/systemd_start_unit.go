// Copyright 2020 Vectorized, Inc.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.md
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0

package commands

import (
	"bufio"
	"fmt"
	"vectorized/pkg/system/systemd"
)

type startSystemdUnitCommand struct {
	client	systemd.Client
	name	string
}

/*
 * Start a systemd unit with the provided name.
 */
func NewStartSystemdUnitCmd(
	client systemd.Client, name string,
) (Command, error) {
	return &startSystemdUnitCommand{client: client, name: name}, nil
}

func (cmd *startSystemdUnitCommand) Execute() error {
	return cmd.client.StartUnit(cmd.name)
}

func (cmd *startSystemdUnitCommand) RenderScript(w *bufio.Writer) error {
	_, err := fmt.Fprintf(w, "sudo systemctl start %s\n", cmd.name)
	return err
}

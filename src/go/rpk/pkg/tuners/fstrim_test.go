// Copyright 2020 Vectorized, Inc.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.md
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0

package tuners

import (
	"errors"
	"strings"
	"testing"
	"time"
	"vectorized/pkg/os"
	"vectorized/pkg/system/systemd"
	"vectorized/pkg/tuners/executors"
	"vectorized/pkg/utils"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

const fstrimBinPath = "/usr/sbin/fstrim"

type mockProc struct {
	fail bool
}

func (p *mockProc) RunWithSystemLdPath(
	_ time.Duration, _ string, _ ...string,
) ([]string, error) {
	if p.fail {
		return []string{}, errors.New("mayor failure over here")
	}
	return []string{fstrimBinPath}, nil
}
func (*mockProc) IsRunning(_ time.Duration, _ string) bool {
	return true
}

func TestTuneFstrimDirectExecutor(t *testing.T) {
	fs := afero.NewMemMapFs()
	exe := executors.NewDirectExecutor()
	shutdown := func() error { return nil }
	startUnit := func(_ string) error { return nil }
	unitState := func(_ string) (systemd.LoadState, systemd.ActiveState, error) {
		return systemd.LoadStateUnknown, systemd.ActiveStateUnknown, nil
	}
	loadUnit := func(_ afero.Fs, _, _ string) error { return nil }
	c := systemd.NewMockClient(shutdown, startUnit, unitState, loadUnit)
	res := tuneFstrim(fs, exe, c, &mockProc{})
	require.NoError(t, res.Error())
}

func TestTuneFstrimDirectExecutorRollback(t *testing.T) {
	// Test that the service is removed if the timer file can't be created.
	fs := afero.NewMemMapFs()
	exe := executors.NewDirectExecutor()
	shutdown := func() error { return nil }
	startUnit := func(_ string) error { return nil }
	unitState := func(_ string) (systemd.LoadState, systemd.ActiveState, error) {
		return systemd.LoadStateUnknown, systemd.ActiveStateUnknown, nil
	}
	errMsg := "nope sorry can't do that"
	loadUnit := func(fs afero.Fs, body, name string) error {
		if name == "redpanda-fstrim.timer" {
			return errors.New(errMsg)
		}
		_, err := utils.WriteBytes(
			fs,
			[]byte(body),
			systemd.UnitPath(name),
		)
		return err
	}
	c := systemd.NewMockClient(shutdown, startUnit, unitState, loadUnit)
	res := tuneFstrim(fs, exe, c, &mockProc{})
	require.EqualError(t, res.Error(), errMsg)

	exists, err := afero.Exists(fs, systemd.UnitPath("redpanda-fstrim.service"))
	require.NoError(t, err)
	require.False(t, exists)
}

func TestTuneFstrimScriptExecutor(t *testing.T) {
	tests := []struct {
		name		string
		shutdown	func() error
		startUnit	func(string) error
		unitState	func(string) (systemd.LoadState, systemd.ActiveState, error)
		loadUnit	func(afero.Fs, string, string) error
		proc		os.Proc
		expected	string
		expectedErrMsg	string
	}{
		{
			name:	"it should install the timer and the service if missing",
			expected: `#!/bin/bash

# Redpanda Tuning Script
# ----------------------------------
# This file was autogenerated by RPK

cat << EOF > /etc/systemd/system/redpanda-fstrim.service
[Unit]
Description=Discard unused blocks on filesystems from /etc/fstab
Documentation=man:fstrim(8)

[Service]
Type=oneshot
ExecStart=` + fstrimBinPath + ` --fstab --verbose --quiet
ProtectSystem=strict
ProtectHome=read-only
PrivateDevices=no
PrivateNetwork=yes
PrivateUsers=no
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
MemoryDenyWriteExecute=yes
SystemCallFilter=@default @file-system @basic-io @system-service

EOF
sudo systemctl daemon-reload
cat << EOF > /etc/systemd/system/redpanda-fstrim.timer
[Unit]
Description=Discard unused blocks once a week
Documentation=man:fstrim

[Timer]
OnCalendar=weekly
AccuracySec=1h
Persistent=true

[Install]
WantedBy=timers.target

EOF
sudo systemctl daemon-reload
sudo systemctl start redpanda-fstrim.timer
`,
		},
		{
			name:	"it should just start the default timer if it exists",
			unitState: func(_ string) (systemd.LoadState, systemd.ActiveState, error) {
				// Mock the units' state as Inactive
				return systemd.LoadStateLoaded, systemd.ActiveStateInactive, nil
			},
			expected: `#!/bin/bash

# Redpanda Tuning Script
# ----------------------------------
# This file was autogenerated by RPK

sudo systemctl start fstrim.timer
`,
		},
		{
			name:	"it should just start the redpanda-fstrim timer if it exists",
			unitState: func(name string) (systemd.LoadState, systemd.ActiveState, error) {
				// Mock the default units' state as Unknown and
				// the redpanda ones as Inactive
				if strings.HasPrefix(name, "redpanda-") {
					return systemd.LoadStateLoaded, systemd.ActiveStateInactive, nil
				}
				return systemd.LoadStateUnknown, systemd.ActiveStateUnknown, nil
			},
			expected: `#!/bin/bash

# Redpanda Tuning Script
# ----------------------------------
# This file was autogenerated by RPK

sudo systemctl start redpanda-fstrim.timer
`,
		},
		{
			name:	"it shouldn't do anything if the default units are started",
			unitState: func(name string) (systemd.LoadState, systemd.ActiveState, error) {
				return systemd.LoadStateLoaded, systemd.ActiveStateActive, nil
			},
			expected: `#!/bin/bash

# Redpanda Tuning Script
# ----------------------------------
# This file was autogenerated by RPK

`,
		},
		{
			name:	"it shouldn't do anything if the redpanda units are started",
			unitState: func(name string) (systemd.LoadState, systemd.ActiveState, error) {
				// Mock the default units' state as Unknown and
				// the redpanda ones as Inactive
				if strings.HasPrefix(name, "redpanda-") {
					return systemd.LoadStateLoaded, systemd.ActiveStateActive, nil
				}
				return systemd.LoadStateUnknown, systemd.ActiveStateUnknown, nil
			},
			expected: `#!/bin/bash

# Redpanda Tuning Script
# ----------------------------------
# This file was autogenerated by RPK

`,
		},
		{
			name:	"it should fail if unitState fails",
			unitState: func(name string) (systemd.LoadState, systemd.ActiveState, error) {
				err := errors.New("unitState error")
				return systemd.LoadStateUnknown, systemd.ActiveStateUnknown, err
			},
			expectedErrMsg:	"unitState error",
		},
		{
			name:		"it should fail if fstrim isn't installed",
			proc:		&mockProc{fail: true},
			expectedErrMsg:	"mayor failure over here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(st *testing.T) {
			shutdown := func() error { return nil }
			startUnit := func(_ string) error { return nil }
			unitState := func(_ string) (systemd.LoadState, systemd.ActiveState, error) {
				return systemd.LoadStateUnknown, systemd.ActiveStateUnknown, nil
			}
			loadUnit := func(_ afero.Fs, _, _ string) error {
				return nil
			}
			fs := afero.NewMemMapFs()
			scriptPath := "tune-redpanda.sh"
			exe := executors.NewScriptRenderingExecutor(
				fs,
				scriptPath,
			)
			if tt.shutdown != nil {
				shutdown = tt.shutdown
			}
			if tt.startUnit != nil {
				startUnit = tt.startUnit
			}
			if tt.unitState != nil {
				unitState = tt.unitState
			}
			if tt.loadUnit != nil {
				loadUnit = tt.loadUnit
			}
			c := systemd.NewMockClient(
				shutdown,
				startUnit,
				unitState,
				loadUnit,
			)
			var proc os.Proc = &mockProc{}
			if tt.proc != nil {
				proc = tt.proc
			}
			res := tuneFstrim(fs, exe, c, proc)
			if tt.expectedErrMsg != "" {
				require.EqualError(t, res.Error(), tt.expectedErrMsg)
				return
			}
			require.NoError(t, res.Error())

			bs, err := afero.ReadFile(fs, scriptPath)
			require.NoError(t, err)
			require.Equal(st, tt.expected, string(bs))
		})
	}
}

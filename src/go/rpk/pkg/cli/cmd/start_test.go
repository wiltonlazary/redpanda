// Copyright 2020 Vectorized, Inc.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.md
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"os"
	"testing"
	"vectorized/pkg/config"
	"vectorized/pkg/redpanda"

	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

type noopLauncher struct{}

func (*noopLauncher) Start(_ string, _ *redpanda.RedpandaArgs) error {
	return nil
}

func TestMergeFlags(t *testing.T) {
	tests := []struct {
		name		string
		current		map[string]interface{}
		overrides	[]string
		expected	map[string]string
	}{
		{
			name:		"it should override the existent values",
			current:	map[string]interface{}{"a": "true", "b": "2", "c": "127.0.0.1"},
			overrides:	[]string{"--a false", "b 42"},
			expected:	map[string]string{"a": "false", "b": "42", "c": "127.0.0.1"},
		}, {
			name:		"it should override the existent values (2)",
			current:	map[string]interface{}{"lock-memory": "true", "cpumask": "0-1", "logger-log-level": "'exception=debug'"},
			overrides: []string{"--overprovisioned", "--unsafe-bypass-fsync 1",
				"--default-log-level=trace", "--logger-log-level='exception=debug'",
				"--fail-on-abandoned-failed-futures"},
			expected: map[string]string{
				"lock-memory":				"true",
				"cpumask":				"0-1",
				"logger-log-level":			"'exception=debug'",
				"overprovisioned":			"",
				"unsafe-bypass-fsync":			"1",
				"default-log-level":			"trace",
				"--fail-on-abandoned-failed-futures":	"",
			},
		}, {
			name:		"it should create values not present in the current flags",
			current:	map[string]interface{}{},
			overrides:	[]string{"b 42", "c 127.0.0.1"},
			expected:	map[string]string{"b": "42", "c": "127.0.0.1"},
		}, {
			name:		"it shouldn't change the current flags if no overrides are given",
			current:	map[string]interface{}{"b": "42", "c": "127.0.0.1"},
			overrides:	[]string{},
			expected:	map[string]string{"b": "42", "c": "127.0.0.1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags := mergeFlags(tt.current, tt.overrides)
			require.Equal(t, len(flags), len(tt.expected))
			if len(flags) != len(tt.expected) {
				t.Fatal("the flags dicts differ in size")
			}

			for k, v := range flags {
				require.Equal(t, tt.expected[k], v)
			}
		})
	}
}

func TestParseSeeds(t *testing.T) {
	tests := []struct {
		name		string
		arg		[]string
		expected	[]config.SeedServer
		expectedErrMsg	string
	}{
		{
			name:	"it should parse well-formed seed addrs",
			arg:	[]string{"127.0.0.1:1234+0", "domain.com:9892+1", "lonely-host+30", "192.168.34.1+5"},
			expected: []config.SeedServer{
				{
					config.SocketAddress{"127.0.0.1", 1234},
					0,
				},
				{
					config.SocketAddress{"domain.com", 9892},
					1,
				},
				{
					config.SocketAddress{"lonely-host", 33145},
					30,
				},
				{
					config.SocketAddress{"192.168.34.1", 33145},
					5,
				},
			},
		},
		{
			name:		"it shouldn't do anything for an empty list",
			arg:		[]string{},
			expected:	[]config.SeedServer{},
		},
		{
			name:		"it should fail for empty addresses",
			arg:		[]string{"+1"},
			expectedErrMsg:	"Couldn't parse seed '+1': empty address",
		},
		{
			name:		"it should fail if one of the addrs is missing an ID",
			arg:		[]string{"127.0.0.1:1234+0", "domain.com"},
			expectedErrMsg:	"Couldn't parse seed 'domain.com': Format doesn't conform to <host>[:<port>]+<id>. Missing ID.",
		},
		{
			name:		"it should fail if one of the addrs' ID isn't an int",
			arg:		[]string{"127.0.0.1:1234+id?", "domain.com+1"},
			expectedErrMsg:	"Couldn't parse seed '127.0.0.1:1234+id?': ID must be an int.",
		},
		{
			name:		"it should fail if the host is empty",
			arg:		[]string{" :1234+1234"},
			expectedErrMsg:	"Couldn't parse seed ' :1234+1234': Empty host in address ' :1234'",
		},
		{
			name:		"it should fail if the port is empty",
			arg:		[]string{" :+1234"},
			expectedErrMsg:	"Couldn't parse seed ' :+1234': Empty host in address ' :'",
		},
		{
			name:		"it should fail if the port is empty",
			arg:		[]string{"host:+1234"},
			expectedErrMsg:	"Couldn't parse seed 'host:+1234': Port must be an int",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(st *testing.T) {
			addrs, err := parseSeeds(tt.arg)
			if tt.expectedErrMsg != "" {
				require.EqualError(st, err, tt.expectedErrMsg)
				return
			}
			require.Exactly(st, tt.expected, addrs)
		})
	}
}

func TestStartCommand(t *testing.T) {
	tests := []struct {
		name		string
		launcher	redpanda.Launcher
		args		[]string
		before		func(afero.Fs) error
		after		func()
		postCheck	func(afero.Fs, *testing.T)
		expectedErrMsg	string
	}{{
		name:	"should fail if the config at the given path is corrupt",
		args:	[]string{"--config", config.Default().ConfigFile},
		before: func(fs afero.Fs) error {
			return afero.WriteFile(
				fs,
				config.Default().ConfigFile,
				[]byte("^&notyaml"),
				0755,
			)
		},
		expectedErrMsg:	"An error happened while trying to read /etc/redpanda/redpanda.yaml: While parsing config: yaml: unmarshal errors:\n  line 1: cannot unmarshal !!str `^&notyaml` into map[string]interface {}",
	}, {
		name:	"should generate the config at the given path if it doesn't exist",
		args: []string{
			"--config", config.Default().ConfigFile,
			"--install-dir", "/var/lib/redpanda",
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			path := config.Default().ConfigFile
			exists, err := afero.Exists(
				fs,
				path,
			)
			require.NoError(st, err)
			require.True(
				st,
				exists,
				"The config should have been created at '%s'",
				path,
			)
			defaultConf := config.Default()
			// The default value for --overprovisioned is true in
			// rpk start
			defaultConf.Rpk.Overprovisioned = true
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(path)
			require.NoError(st, err)
			// Check that the generated config is as expected.
			require.Exactly(st, defaultConf, conf)
		},
	}, {
		name:	"it should parse the --seeds and persist them",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
			"--seeds", "192.168.34.32:33145+1,somehost:54321+3,justahostnoport+5",
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedSeeds := []config.SeedServer{{
				Host: config.SocketAddress{
					Address:	"192.168.34.32",
					Port:		33145,
				},
				Id:	1,
			}, {
				Host: config.SocketAddress{
					Address:	"somehost",
					Port:		54321,
				},
				Id:	3,
			}, {
				Host: config.SocketAddress{
					Address:	"justahostnoport",
					Port:		33145,
				},
				Id:	5,
			}}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedSeeds,
				conf.Redpanda.SeedServers,
			)
		},
	}, {
		name:	"it should parse the --seeds and persist them (shorthand)",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
			"-s", "192.168.3.32:33145+1",
			"-s", "192.168.123.32:33146+5,host+34",
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedSeeds := []config.SeedServer{{
				Host: config.SocketAddress{
					Address:	"192.168.3.32",
					Port:		33145,
				},
				Id:	1,
			}, {
				Host: config.SocketAddress{
					Address:	"192.168.123.32",
					Port:		33146,
				},
				Id:	5,
			}, {
				Host: config.SocketAddress{
					Address:	"host",
					Port:		33145,
				},
				Id:	34,
			}}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedSeeds,
				conf.Redpanda.SeedServers,
			)
		},
	}, {
		name:	"if --seeds wasn't passed, it should fall back to REDPANDA_SEEDS and persist it",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
		},
		before: func(_ afero.Fs) error {
			os.Setenv("REDPANDA_SEEDS", "10.23.12.5:33146+5,host+34")
			return nil
		},
		after: func() {
			os.Unsetenv("REDPANDA_SEEDS")
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedSeeds := []config.SeedServer{{
				Host: config.SocketAddress{
					Address:	"10.23.12.5",
					Port:		33146,
				},
				Id:	5,
			}, {
				Host: config.SocketAddress{
					Address:	"host",
					Port:		33145,
				},
				Id:	34,
			}}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedSeeds,
				conf.Redpanda.SeedServers,
			)
		},
	}, {
		name:	"it should leave existing seeds untouched if --seeds or REDPANDA_SEEDS aren't set",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
		},
		before: func(fs afero.Fs) error {
			mgr := config.NewManager(fs)
			conf := config.Default()
			conf.Redpanda.SeedServers = []config.SeedServer{{
				Host: config.SocketAddress{
					Address:	"10.23.12.5",
					Port:		33146,
				},
				Id:	5,
			}}
			return mgr.Write(conf)
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedSeeds := []config.SeedServer{{
				Host: config.SocketAddress{
					Address:	"10.23.12.5",
					Port:		33146,
				},
				Id:	5,
			}}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedSeeds,
				conf.Redpanda.SeedServers,
			)
		},
	}, {
		name:	"it should fail if the host is missing in the given seed",
		args: []string{
			"-s", "goodhost.com:54897+2,:33145+1",
		},
		expectedErrMsg:	"Couldn't parse seed ':33145+1': Empty host in address ':33145'",
	}, {
		name:	"it should fail if the ID is missing in the given seed",
		args: []string{
			"-s", "host:33145",
		},
		expectedErrMsg:	"Couldn't parse seed 'host:33145': Format doesn't conform to <host>[:<port>]+<id>. Missing ID.",
	}, {
		name:	"it should fail if the port isn't an int",
		args: []string{
			"-s", "host:port+2",
		},
		expectedErrMsg:	"Couldn't parse seed 'host:port+2': Port must be an int",
	}, {
		name:	"it should parse the --rpc-addr and persist it",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
			"--rpc-addr", "192.168.34.32:33145",
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := config.SocketAddress{
				Address:	"192.168.34.32",
				Port:		33145,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.RPCServer,
			)
		},
	}, {
		name:	"it should parse the --rpc-addr and persist it (no port)",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
			"--rpc-addr", "192.168.34.32",
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := config.SocketAddress{
				Address:	"192.168.34.32",
				Port:		33145,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.RPCServer,
			)
		},
	}, {
		name:	"it should fail if --rpc-addr is invalid",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
			"--rpc-addr", "host:nonnumericport",
		},
		expectedErrMsg:	"Port must be an int",
	}, {
		name:	"if --rpc-addr wasn't passed, it should fall back to REDPANDA_RPC_ADDRESS and persist it",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
		},
		before: func(_ afero.Fs) error {
			os.Setenv("REDPANDA_RPC_ADDRESS", "host:3123")
			return nil
		},
		after: func() {
			os.Unsetenv("REDPANDA_RPC_ADDRESS")
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := config.SocketAddress{
				Address:	"host",
				Port:		3123,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.RPCServer,
			)
		},
	}, {
		name:	"it should leave the RPC addr untouched if the env var & flag weren't set",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
		},
		before: func(fs afero.Fs) error {
			mgr := config.NewManager(fs)
			conf := config.Default()
			conf.Redpanda.RPCServer = config.SocketAddress{
				Address:	"192.168.33.33",
				Port:		9892,
			}
			return mgr.Write(conf)
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := config.SocketAddress{
				Address:	"192.168.33.33",
				Port:		9892,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.RPCServer,
			)
		},
	}, {
		name:	"it should parse the --kafka-addr and persist it",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
			"--kafka-addr", "192.168.34.32:33145",
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := config.SocketAddress{
				Address:	"192.168.34.32",
				Port:		33145,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.KafkaApi,
			)
		},
	}, {
		name:	"it should parse the --kafka-addr and persist it (no port)",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
			"--kafka-addr", "192.168.34.32",
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := config.SocketAddress{
				Address:	"192.168.34.32",
				Port:		9092,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.KafkaApi,
			)
		},
	}, {
		name:	"it should fail if --kafka-addr is invalid",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
			"--kafka-addr", "host:nonnumericport",
		},
		expectedErrMsg:	"Port must be an int",
	}, {
		name:	"if --kafka-addr wasn't passed, it should fall back to REDPANDA_KAFKA_ADDRESS and persist it",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
		},
		before: func(_ afero.Fs) error {
			os.Setenv("REDPANDA_KAFKA_ADDRESS", "host:3123")
			return nil
		},
		after: func() {
			os.Unsetenv("REDPANDA_KAFKA_ADDRESS")
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := config.SocketAddress{
				Address:	"host",
				Port:		3123,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.KafkaApi,
			)
		},
	}, {
		name:	"it should leave the Kafka addr untouched if the env var & flag weren't set",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
		},
		before: func(fs afero.Fs) error {
			mgr := config.NewManager(fs)
			conf := config.Default()
			conf.Redpanda.KafkaApi = config.SocketAddress{
				Address:	"192.168.33.33",
				Port:		9892,
			}
			return mgr.Write(conf)
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := config.SocketAddress{
				Address:	"192.168.33.33",
				Port:		9892,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.KafkaApi,
			)
		},
	}, {
		name:	"it should parse the --advertise-kafka-addr and persist it",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
			"--advertise-kafka-addr", "192.168.34.32:33145",
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := &config.SocketAddress{
				Address:	"192.168.34.32",
				Port:		33145,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.AdvertisedKafkaApi,
			)
		},
	}, {
		name:	"it should parse the --advertise-kafka-addr and persist it (no port)",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
			"--advertise-kafka-addr", "192.168.34.32",
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := &config.SocketAddress{
				Address:	"192.168.34.32",
				Port:		9092,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.AdvertisedKafkaApi,
			)
		},
	}, {
		name:	"it should fail if --advertise-kafka-addr is invalid",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
			"--advertise-kafka-addr", "host:nonnumericport",
		},
		expectedErrMsg:	"Port must be an int",
	}, {
		name:	"if --advertise-kafka-addr, it should fall back to REDPANDA_ADVERTISE_KAFKA_ADDRESS and persist it",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
		},
		before: func(_ afero.Fs) error {
			os.Setenv("REDPANDA_ADVERTISE_KAFKA_ADDRESS", "host:3123")
			return nil
		},
		after: func() {
			os.Unsetenv("REDPANDA_ADVERTISE_KAFKA_ADDRESS")
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := &config.SocketAddress{
				Address:	"host",
				Port:		3123,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.AdvertisedKafkaApi,
			)
		},
	}, {
		name:	"it should leave the adv. Kafka addr untouched if the env var & flag weren't set",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
		},
		before: func(fs afero.Fs) error {
			mgr := config.NewManager(fs)
			conf := config.Default()
			conf.Redpanda.AdvertisedKafkaApi = &config.SocketAddress{
				Address:	"192.168.33.33",
				Port:		9892,
			}
			return mgr.Write(conf)
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := &config.SocketAddress{
				Address:	"192.168.33.33",
				Port:		9892,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.AdvertisedKafkaApi,
			)
		},
	}, {
		name:	"it should parse the --advertise-rpc-addr and persist it",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
			"--advertise-rpc-addr", "192.168.34.32:33145",
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := &config.SocketAddress{
				Address:	"192.168.34.32",
				Port:		33145,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.AdvertisedRPCAPI,
			)
		},
	}, {
		name:	"it should parse the --advertise-rpc-addr and persist it (no port)",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
			"--advertise-rpc-addr", "192.168.34.32",
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := &config.SocketAddress{
				Address:	"192.168.34.32",
				Port:		33145,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.AdvertisedRPCAPI,
			)
		},
	}, {
		name:	"it should fail if --advertise-rpc-addr is invalid",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
			"--advertise-rpc-addr", "host:nonnumericport",
		},
		expectedErrMsg:	"Port must be an int",
	}, {
		name:	"if --advertise-rpc-addr wasn't passed, it should fall back to REDPANDA_ADVERTISE_RPC_ADDRESS and persist it",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
		},
		before: func(_ afero.Fs) error {
			os.Setenv("REDPANDA_ADVERTISE_RPC_ADDRESS", "host:3123")
			return nil
		},
		after: func() {
			os.Unsetenv("REDPANDA_ADVERTISE_RPC_ADDRESS")
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := &config.SocketAddress{
				Address:	"host",
				Port:		3123,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.AdvertisedRPCAPI,
			)
		},
	}, {
		name:	"it should leave the adv. RPC addr untouched if the env var & flag weren't set",
		args: []string{
			"--install-dir", "/var/lib/redpanda",
		},
		before: func(fs afero.Fs) error {
			mgr := config.NewManager(fs)
			conf := config.Default()
			conf.Redpanda.AdvertisedRPCAPI = &config.SocketAddress{
				Address:	"192.168.33.33",
				Port:		9892,
			}
			return mgr.Write(conf)
		},
		postCheck: func(fs afero.Fs, st *testing.T) {
			mgr := config.NewManager(fs)
			conf, err := mgr.Read(config.Default().ConfigFile)
			require.NoError(st, err)
			expectedAddr := &config.SocketAddress{
				Address:	"192.168.33.33",
				Port:		9892,
			}
			// Check that the generated config is as expected.
			require.Exactly(
				st,
				expectedAddr,
				conf.Redpanda.AdvertisedRPCAPI,
			)
		},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(st *testing.T) {
			if tt.after != nil {
				defer tt.after()
			}
			fs := afero.NewMemMapFs()
			mgr := config.NewManager(fs)
			var launcher redpanda.Launcher = &noopLauncher{}
			if tt.launcher != nil {
				launcher = tt.launcher
			}
			if tt.before != nil {
				require.NoError(st, tt.before(fs))
			}
			var out bytes.Buffer
			logrus.SetOutput(&out)
			c := NewStartCommand(fs, mgr, launcher)
			c.SetArgs(tt.args)
			err := c.Execute()
			if tt.expectedErrMsg != "" {
				require.EqualError(st, err, tt.expectedErrMsg)
				return
			}
			require.NoError(st, err)
			if tt.postCheck != nil {
				tt.postCheck(fs, st)
			}
		})
	}
}

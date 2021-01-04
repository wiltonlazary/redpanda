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
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"vectorized/pkg/api"
	"vectorized/pkg/cli/cmd/version"
	"vectorized/pkg/cli/ui"
	"vectorized/pkg/cloud"
	"vectorized/pkg/config"
	"vectorized/pkg/kafka"
	"vectorized/pkg/system"

	"github.com/Shopify/sarama"
	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
)

type metricsResult struct {
	rows	[][]string
	metrics	*system.Metrics
}

func NewStatusCommand(fs afero.Fs, mgr config.Manager) *cobra.Command {
	var (
		configFile	string
		send		bool
		timeout		time.Duration
	)
	command := &cobra.Command{
		Use:		"status",
		Short:		"Check the resource usage in the system, and optionally send it to Vectorized",
		Long:		"",
		SilenceUsage:	true,
		RunE: func(ccmd *cobra.Command, args []string) error {
			return executeStatus(fs, mgr, configFile, timeout, send)
		},
	}
	command.Flags().StringVar(
		&configFile,
		"config",
		"",
		"Redpanda config file, if not set the file will be searched for"+
			" in the default locations",
	)
	command.Flags().BoolVar(
		&send,
		"send",
		false,
		"Tells `status` whether to send the gathered resource usage data to Vectorized")
	command.Flags().DurationVar(
		&timeout,
		"timeout",
		2000*time.Millisecond,
		"The maximum amount of time to wait for the metrics to be gathered. "+
			"The value passed is a sequence of decimal numbers, each with optional "+
			"fraction and a unit suffix, such as '300ms', '1.5s' or '2h45m'. "+
			"Valid time units are 'ns', 'us' (or 'µs'), 'ms', 's', 'm', 'h'",
	)
	return command
}

func executeStatus(
	fs afero.Fs,
	mgr config.Manager,
	configFile string,
	timeout time.Duration,
	send bool,
) error {
	conf, err := mgr.FindOrGenerate(configFile)
	if err != nil {
		return err
	}
	if !conf.Rpk.EnableUsageStats && send {
		log.Debug("Usage stats reporting is disabled, so nothing will" +
			" be sent. To enable it, run" +
			" `rpk config set rpk.enable_usage_stats true`.")
	}
	t := ui.NewRpkTable(log.StandardLogger().Out)
	t.SetColWidth(80)
	t.SetAutoWrapText(true)
	t.Append(getVersion())

	providerInfoRowsCh := make(chan [][]string)
	osInfoRowsCh := make(chan [][]string)
	cpuInfoRowsCh := make(chan [][]string)
	confRowsCh := make(chan [][]string)
	kafkaRowsCh := make(chan [][]string)

	metricsRes, err := getMetrics(fs, mgr, timeout, *conf)
	if err != nil {
		// Retrieving the metrics is a prerequisite to sending them.
		// Therefore, if that fails, return an error.
		if send {
			return err
		}
		// Otherwise, just log it. The rest of the info will still be
		// shown to the user.
		log.Infof("%v", err)
	} else if send {
		// If there was no error, send the metrics.
		err := sendMetrics(*conf, metricsRes.metrics)
		if err != nil {
			return fmt.Errorf("Error sending metrics: %v", err)
		}
		return nil
	}

	grp := multierror.Group{}
	grp.Go(func() error {
		return getCloudProviderInfo(providerInfoRowsCh)
	})
	grp.Go(func() error {
		return getOSInfo(timeout, osInfoRowsCh)
	})
	grp.Go(func() error {
		return getCPUInfo(cpuInfoRowsCh)
	})
	grp.Go(func() error {
		return getConf(mgr, conf.ConfigFile, confRowsCh)
	})
	grp.Go(func() error {
		return getKafkaInfo(*conf, kafkaRowsCh)
	})
	results := [][][]string{
		metricsRes.rows,
		<-osInfoRowsCh,
		<-cpuInfoRowsCh,
		<-providerInfoRowsCh,
		<-confRowsCh,
		<-kafkaRowsCh,
	}

	errs := grp.Wait().Errors
	if errs != nil {
		for _, err := range errs {
			log.Info(err)
		}
	}
	for _, rows := range results {
		for _, row := range rows {
			t.Append(row)
		}
	}
	t.Render()

	return nil
}

func getVersion() []string {
	return []string{"Version", version.Pretty()}
}

func getCloudProviderInfo(out chan<- [][]string) error {
	v, err := cloud.AvailableVendor()
	if err != nil {
		out <- [][]string{}
		return errors.Wrap(err, "Error initializing")
	}
	rows := [][]string{{"Cloud Provider", v.Name()}}
	vmType, err := v.VmType()
	if err != nil {
		err = errors.Wrap(err, "Error getting the VM type")
	} else {
		rows = append(rows, []string{"Machine Type", vmType})
	}
	out <- rows
	return err
}

func getMetrics(
	fs afero.Fs, mgr config.Manager, timeout time.Duration, conf config.Config,
) (*metricsResult, error) {
	res := &metricsResult{[][]string{}, nil}
	m, errs := system.GatherMetrics(fs, timeout, conf)
	if len(errs) != 0 {
		err := multierror.Append(nil, errs...)
		return res, errors.Wrap(err, "Error gathering metrics")
	}
	res.metrics = m
	res.rows = append(
		res.rows,
		[]string{"CPU Usage %", fmt.Sprintf("%0.3f", m.CpuPercentage)},
		[]string{"Free Memory (MB)", fmt.Sprintf("%0.3f", m.FreeMemoryMB)},
		[]string{"Free Space  (MB)", fmt.Sprintf("%0.3f", m.FreeSpaceMB)},
	)
	return res, nil
}

func getOSInfo(timeout time.Duration, out chan<- [][]string) error {
	rows := [][]string{}
	osInfo, err := system.UnameAndDistro(timeout)
	if err != nil {
		err = errors.Wrap(err, "Error querying OS info")
	} else {
		rows = append(rows, []string{"OS", osInfo})
	}
	out <- rows
	return err
}

func getCPUInfo(out chan<- [][]string) error {
	rows := [][]string{}
	cpuInfo, err := system.CpuInfo()
	if err != nil {
		err = errors.Wrap(err, "Error querying CPU info")
	}
	cpuModel := ""
	if len(cpuInfo) > 0 {
		cpuModel = cpuInfo[0].ModelName
		rows = append(rows, []string{"CPU Model", cpuModel})
	}
	out <- rows
	return err
}

func getConf(
	mgr config.Manager, configFile string, out chan<- [][]string,
) error {
	rows := [][]string{}
	props, err := mgr.ReadFlat(configFile)
	if err != nil {
		err = errors.Wrap(err, "Error reading or parsing configuration")
	} else {
		keys := []string{}
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			rows = append(rows, []string{k, props[k]})
		}
	}
	out <- rows
	return err
}

func getKafkaInfo(conf config.Config, out chan<- [][]string) error {
	addr := fmt.Sprintf(
		"%s:%d",
		conf.Redpanda.KafkaApi.Address,
		conf.Redpanda.KafkaApi.Port,
	)
	client, err := kafka.InitClientWithConf(&conf, addr)
	if err != nil {
		out <- [][]string{}
		return errors.Wrap(err, "Error initializing redpanda client")
	}
	admin, err := sarama.NewClusterAdminFromClient(client)
	if err != nil {
		out <- [][]string{}
		return errors.Wrap(err, "Error initializing redpanda client")
	}
	defer admin.Close()
	topics, err := topicsDetail(admin)
	if err != nil {
		out <- [][]string{}
		return errors.Wrap(err, "Error fetching the Redpanda topic details")
	}
	if len(topics) == 0 {
		out <- [][]string{}
		return nil
	}
	out <- getKafkaInfoRows(client.Brokers(), topics)
	return nil
}

func getKafkaInfoRows(
	brokers []*sarama.Broker, topics []*sarama.TopicMetadata,
) [][]string {
	rows := [][]string{}
	spacingRow := []string{"", ""}
	type node struct {
		// map[topic-name][]partitions
		leaderParts	map[string][]int
		replicaParts	map[string][]int
	}
	nodePartitions := map[int]*node{}
	for _, topic := range topics {
		for _, p := range topic.Partitions {
			leaderID := int(p.Leader)
			n := nodePartitions[leaderID]
			if n != nil {
				topicParts := n.leaderParts[topic.Name]
				topicParts = append(topicParts, int(p.ID))
				if n.leaderParts == nil {
					n.leaderParts = map[string][]int{}
				}
				n.leaderParts[topic.Name] = topicParts
			} else {
				leaderParts := map[string][]int{}
				leaderParts[topic.Name] = []int{int(p.ID)}
				nodePartitions[leaderID] = &node{
					leaderParts: leaderParts,
				}
			}

			for _, r := range p.Replicas {
				replicaID := int(r)
				// Don't list leaders as replicas of their partitions
				if replicaID == leaderID {
					continue
				}
				n := nodePartitions[replicaID]
				if n != nil {
					topicParts := n.replicaParts[topic.Name]
					topicParts = append(topicParts, int(p.ID))
					if n.replicaParts == nil {
						n.replicaParts = map[string][]int{}
					}
					n.replicaParts[topic.Name] = topicParts
				} else {
					replicaParts := map[string][]int{}
					replicaParts[topic.Name] = []int{int(p.ID)}
					nodePartitions[replicaID] = &node{
						replicaParts: replicaParts,
					}
				}
			}
		}
	}
	idToBroker := map[int]sarama.Broker{}
	for _, broker := range brokers {
		if broker != nil {
			idToBroker[int(broker.ID())] = *broker
		}
	}
	nodeIDs := []int{}
	for nodeID := range nodePartitions {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Ints(nodeIDs)
	rows = append(
		rows,
		[]string{"", ""},
		[]string{"Redpanda Cluster Status", ""},
		[]string{"Node ID (IP)", "Partitions"},
	)
	for _, nodeID := range nodeIDs {
		node := nodePartitions[nodeID]
		broker := idToBroker[nodeID]

		if nodeID < 0 {
			// A negative node ID means the partitions haven't
			// been assigned a leader
			leaderlessRow := []string{
				"(Leaderless)",
				formatTopicsAndPartitions(node.leaderParts),
			}
			rows = append(
				rows,
				leaderlessRow,
				spacingRow,
			)
			continue
		}
		nodeInfo := fmt.Sprintf("%d (%s)", nodeID, broker.Addr())
		leaderParts := formatTopicsAndPartitions(node.leaderParts)
		leaderRow := []string{
			nodeInfo,
			"Leader: " + leaderParts,
		}
		replicaParts := formatTopicsAndPartitions(node.replicaParts)
		replicaRow := []string{
			"",
			"Replica: " + replicaParts,
		}
		rows = append(
			rows,
			leaderRow,
			spacingRow,
			replicaRow,
			spacingRow,
		)
	}
	return rows
}

func sendMetrics(conf config.Config, metrics *system.Metrics) error {
	payload := api.MetricsPayload{
		FreeMemoryMB:	metrics.FreeMemoryMB,
		FreeSpaceMB:	metrics.FreeSpaceMB,
		CpuPercentage:	metrics.CpuPercentage,
	}
	return api.SendMetrics(payload, conf)
}

func topicsDetail(admin sarama.ClusterAdmin) ([]*sarama.TopicMetadata, error) {
	topics, err := admin.ListTopics()
	if err != nil {
		return nil, err
	}
	topicNames := []string{}
	for name := range topics {
		topicNames = append(topicNames, name)
	}
	return admin.DescribeTopics(topicNames)
}

func formatTopicsAndPartitions(tps map[string][]int) string {
	topicNames := []string{}
	for topicName := range tps {
		topicNames = append(topicNames, topicName)
	}
	sort.Strings(topicNames)
	buf := []string{}
	for _, topicName := range topicNames {
		parts := tps[topicName]
		buf = append(buf, formatTopicPartitions(topicName, parts))
	}
	return strings.Join(buf, "; ")
}

func formatTopicPartitions(name string, partitions []int) string {
	limit := 50
	partitionsNo := len(partitions)
	if partitionsNo <= limit {
		// If the number of partitions is small enough, we can display
		// them all.
		strParts := compress(partitions)
		return fmt.Sprintf("%s: [%s]", name, strings.Join(strParts, ", "))
	}
	// When the # of partitions is too big, the ouput becomes unreadable,
	// so it needs to be truncated.
	return fmt.Sprintf(
		"%s: (%d partitions)",
		name,
		partitionsNo,
	)
}

func compress(is []int) []string {
	length := len(is)
	if length == 0 {
		return []string{}
	}
	sort.Ints(is)
	ranges := []string{}
	for i := 0; i < length; i++ {
		low := is[i]
		high := low
		j := i + 1
		index := j
		for j := i + 1; j < length && is[j] == high+1; j++ {
			high = is[j]
			index = j
		}
		switch {
		case low == high:
			// If there was no range, just add the number.
			ranges = append(ranges, strconv.Itoa(low))
		case high == low+1:
			// If the range is only n - n+1, it makes no sense to
			// add a hyphen.
			ranges = append(
				ranges,
				strconv.Itoa(low),
				strconv.Itoa(high),
			)
			i = index
		default:
			ranges = append(ranges, fmt.Sprintf("%d-%d", low, high))
			i = index
		}
	}
	return ranges
}

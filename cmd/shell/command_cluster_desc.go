package shell

import (
	"context"
	"fmt"
	"io"

	"github.com/chrislusf/vasto/goclient/vs"
	"github.com/chrislusf/vasto/pb"
)

func init() {
	commands = append(commands, &commandDesc{})
}

type commandDesc struct {
}

func (c *commandDesc) Name() string {
	return "cluster.desc"
}

func (c *commandDesc) Help() string {
	return "[<cluster_name>]"
}

func (c *commandDesc) Do(vastoClient *vs.VastoClient, args []string, commandEnv *commandEnv, out io.Writer) error {

	param := ""
	if len(args) > 0 {
		param = args[0]
	}
	if param == "" {
		{
			descResponse, err := vastoClient.MasterClient.Describe(
				context.Background(),
				&pb.DescribeRequest{
					DescDataCenters: &pb.DescribeRequest_DescDataCenters{},
				},
			)

			if err != nil {
				return err
			}

			fmt.Fprintf(out, "available servers:\n")
			for _, server := range descResponse.DescDataCenter.DataCenter.StoreResources {
				fmt.Fprintf(out, "    server %v total:%d GB, allocated:%d GB, Tags:%s\n",
					server.Address, server.DiskSizeGb, server.AllocatedSizeGb, server.Tags)
			}

		}

		{
			descResponse, err := vastoClient.MasterClient.Describe(
				context.Background(),
				&pb.DescribeRequest{
					DescKeyspaces: &pb.DescribeRequest_DescKeyspaces{},
				},
			)

			if err != nil {
				return err
			}

			keyspaces := descResponse.DescKeyspaces.Keyspaces
			for _, keyspace := range keyspaces {
				fmt.Fprintf(out, "keyspace %v client:%d\n", keyspace.Keyspace, keyspace.ClientCount)
				for _, cluster := range keyspace.Clusters {
					fmt.Fprintf(out, "    cluster expected size %d\n", cluster.ExpectedClusterSize)
					for _, node := range cluster.Nodes {
						fmt.Fprintf(out, "        * node %v shard %v %v\n",
							node.ShardInfo.ServerId, node.ShardInfo.ShardId, node.StoreResource.Address)
					}
				}
			}

		}
	} else if len(args) == 1 {

		descResponse, err := vastoClient.MasterClient.Describe(
			context.Background(),
			&pb.DescribeRequest{
				DescCluster: &pb.DescribeRequest_DescCluster{
					Keyspace: param,
				},
			},
		)

		if err != nil {
			return err
		}

		if descResponse.DescCluster == nil {
			return fmt.Errorf("no cluster keyspace(%v) found", param)
		}

		fmt.Fprintf(out, "Cluster Client Count : %d\n", descResponse.DescCluster.ClientCount)
		printCluster(out, descResponse.DescCluster.GetCluster())
		if descResponse.DescCluster.GetNextCluster() != nil {
			nextCluster := descResponse.DescCluster.GetNextCluster()
			fmt.Fprintf(out, "=> Cluster Size: %d\n", nextCluster.ExpectedClusterSize)
			if nextCluster.ExpectedClusterSize >= descResponse.DescCluster.GetCluster().ExpectedClusterSize {
				for _, node := range nextCluster.Nodes {
					if node.StoreResource.Address == "" {
						continue
					}
					fmt.Fprintf(out, "        + node %v shard %v %v\n",
						node.ShardInfo.ServerId, node.ShardInfo.ShardId, node.StoreResource.Address)
				}
			}
		}

	}

	return nil
}

package store

import (
	"fmt"
	"github.com/chrislusf/glog"
	"github.com/chrislusf/vasto/pb"
	"github.com/chrislusf/vasto/topology"
	"golang.org/x/net/context"
	"os"
)

// CreateShard
// 1. if the shard is already created, do nothing
func (ss *storeServer) CreateShard(ctx context.Context, request *pb.CreateShardRequest) (*pb.CreateShardResponse, error) {

	glog.V(1).Infof("%s create shard %v", ss.storeName, request)
	err := ss.createShards(request.Keyspace, int(request.ServerId), int(request.ClusterSize), int(request.ReplicationFactor), false, func(shardId int) *topology.BootstrapPlan {
		return &topology.BootstrapPlan{
			ToClusterSize: int(request.ClusterSize),
		}
	})
	if err != nil {
		glog.Errorf("%s create keyspace %s: %v", ss.storeName, request.Keyspace, err)
		return &pb.CreateShardResponse{
			Error: err.Error(),
		}, nil
	}

	return &pb.CreateShardResponse{
		Error: "",
	}, nil

}

func (ss *storeServer) createShards(keyspace string, serverId int, clusterSize, replicationFactor int, isCandidate bool, planGen func(shardId int) *topology.BootstrapPlan) error {

	var existingPrimaryShards []*pb.ClusterNode
	if cluster, found := ss.clusterListener.GetCluster(keyspace); found {
		for i := 0; i < cluster.ExpectedSize(); i++ {
			if n, ok := cluster.GetNode(i, 0); ok {
				existingPrimaryShards = append(existingPrimaryShards, n)
			} else {
				glog.Errorf("missing server %d", i)
			}
		}
		glog.V(1).Infof("%s existing shards: %+v", ss.storeName, existingPrimaryShards)
	}

	ss.clusterListener.AddNewKeyspace(keyspace, clusterSize, replicationFactor)

	if _, found := ss.keyspaceShards.getShards(keyspace); found {
		localShards, foundLocalShards := ss.getServerStatusInCluster(keyspace)
		if !foundLocalShards {
			return fmt.Errorf("%s missing local shard status for keyspace %s", ss.storeName, keyspace)
		}
		if int(localShards.ClusterSize) == clusterSize && int(localShards.ReplicationFactor) == replicationFactor {
			return fmt.Errorf("%s keyspace %s already exists", ss.storeName, keyspace)
		}
		if serverId != int(localShards.Id) {
			return fmt.Errorf("%s local server id = %d, not matching requested server id %d", ss.storeName, localShards.Id, serverId)
		}
	}

	localShards := ss.getOrCreateServerStatusInCluster(keyspace, serverId, clusterSize, replicationFactor)

	for _, clusterShard := range topology.LocalShards(serverId, clusterSize, replicationFactor) {

		shardInfo, foundShardInfo := localShards.ShardMap[uint32(clusterShard.ShardId)]

		if !foundShardInfo {
			shardInfo = &pb.ShardInfo{
				ServerId:          uint32(serverId),
				ShardId:           uint32(clusterShard.ShardId),
				KeyspaceName:      keyspace,
				ClusterSize:       uint32(clusterSize),
				ReplicationFactor: uint32(replicationFactor),
				IsCandidate:       isCandidate,
			}
		}

		shard, foundShard := ss.keyspaceShards.getShard(keyspace, VastoShardId(clusterShard.ShardId))
		if !foundShard {
			glog.V(1).Infof("%s creating new shard %s", ss.storeName, shardInfo.IdentifierOnThisServer())
			var shardCreationError error
			if shard, shardCreationError = ss.openShard(shardInfo); shardCreationError != nil {
				return fmt.Errorf("creating %s: %v", shardInfo.IdentifierOnThisServer(), shardCreationError)
			}
			glog.V(1).Infof("%s created new shard %s", ss.storeName, shard.String())
		} else {
			glog.V(1).Infof("%s found existing shard %s", ss.storeName, shard.String())
		}

		plan := planGen(clusterShard.ShardId)
		glog.V(1).Infof("%s shard %s bootstrap plan: %s", ss.storeName, shardInfo.IdentifierOnThisServer(), plan.String())

		if err := shard.startWithBootstrapPlan(plan, ss.selfAdminAddress(), existingPrimaryShards); err != nil {
			return fmt.Errorf("%s bootstrap shard %v : %v", ss.storeName, shardInfo.IdentifierOnThisServer(), err)
		}

		localShards.ShardMap[uint32(clusterShard.ShardId)] = shardInfo

		if !foundShardInfo {
			ss.sendShardInfoToMaster(shardInfo, pb.ShardInfo_READY)
		}

	}

	return ss.saveClusterConfig(localShards, keyspace)

}

func (ss *storeServer) startExistingNodes(keyspaceName string, storeStatus *pb.LocalShardsInCluster) error {
	for _, shardInfo := range storeStatus.ShardMap {
		shard, shardOpenError := ss.openShard(shardInfo)
		if shardOpenError != nil {
			return fmt.Errorf("%s open %s: %v", ss.storeName, shardInfo.IdentifierOnThisServer(), shardOpenError)
		}

		for fileId, meta := range shard.db.GetLiveFilesMetaData() {
			glog.V(1).Infof("%s %d name:%s, level:%d size:%d SmallestKey:%s LargestKey:%s", ss.storeName, fileId, meta.Name, meta.Level, meta.Size, string(meta.SmallestKey), string(meta.LargestKey))
			if meta.Level >= 6 {
				shard.hasBackfilled = true
			}
		}

		if err := shard.startWithBootstrapPlan(&topology.BootstrapPlan{
			ToClusterSize: int(shardInfo.ClusterSize),
		}, ss.selfAdminAddress(), nil); err != nil {
			return fmt.Errorf("%s bootstrap shard %v : %v", ss.storeName, shardInfo.IdentifierOnThisServer(), err)
		}

	}
	return nil
}

func (ss *storeServer) openShard(shardInfo *pb.ShardInfo) (shard *shard, err error) {

	cluster := ss.clusterListener.GetOrSetCluster(shardInfo.KeyspaceName, int(shardInfo.ClusterSize), int(shardInfo.ReplicationFactor))

	dir := fmt.Sprintf("%s/%s/%d", *ss.option.Dir, shardInfo.KeyspaceName, shardInfo.ShardId)
	err = os.MkdirAll(dir, 0755)
	if err != nil {
		glog.V(1).Infof("%s mkdir %s: %v", ss.storeName, dir, err)
		return nil, fmt.Errorf("%s mkdir %s: %v", ss.storeName, dir, err)
	}

	shard = newShard(shardInfo.KeyspaceName, dir, int(shardInfo.ServerId), int(shardInfo.ShardId), cluster, ss.clusterListener,
		int(shardInfo.ReplicationFactor), *ss.option.LogFileSizeMb, *ss.option.LogFileCount)
	shard.setCompactionFilterClusterSize(int(shardInfo.ClusterSize))
	// println("loading shard", shard.String())
	ss.keyspaceShards.addShards(shardInfo.KeyspaceName, shard)
	ss.RegisterPeriodicTask(shard)
	return shard, nil

}

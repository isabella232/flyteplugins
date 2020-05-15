package hive

import (
	"context"
	"fmt"
	"reflect"
	"strconv"

	"github.com/lyft/flytestdlib/logger"

	"github.com/lyft/flyteplugins/go/tasks/errors"

	"github.com/lyft/flyteplugins/go/tasks/plugins/hive/client"
	"github.com/lyft/flyteplugins/go/tasks/plugins/hive/config"

	pluginMachinery "github.com/lyft/flyteplugins/go/tasks/pluginmachinery"
	"github.com/lyft/flyteplugins/go/tasks/pluginmachinery/core"
	"github.com/lyft/flyteplugins/go/tasks/pluginmachinery/remote"
)

type QuboleHivePlugin struct {
	client         client.QuboleClient
	apiKey         string
	resourceQuotas map[core.ResourceNamespace]int
	properties     remote.PluginProperties
}

func (q QuboleHivePlugin) GetPluginProperties() remote.PluginProperties {
	return q.properties
}

func (q QuboleHivePlugin) ResourceRequirements(ctx context.Context, tCtx remote.TaskExecutionContext) (
	namespace core.ResourceNamespace, constraints core.ResourceConstraintsSpec, err error) {
	uniqueID := tCtx.TaskExecutionMetadata().GetTaskExecutionID().GetGeneratedName()

	clusterPrimaryLabel, err := composeResourceNamespaceWithClusterPrimaryLabel(ctx, tCtx)
	if err != nil {
		return "", core.ResourceConstraintsSpec{}, errors.Wrapf(errors.ResourceManagerFailure, err, "Error getting query info when requesting allocation token %s", uniqueID)
	}

	resourceConstraintsSpec := createResourceConstraintsSpec(ctx, tCtx, clusterPrimaryLabel)
	return clusterPrimaryLabel, resourceConstraintsSpec, nil
}

func (q QuboleHivePlugin) Create(ctx context.Context, tCtx remote.TaskExecutionContext) (
	createdResources remote.ResourceMeta, err error) {
	query, clusterLabelOverride, tags, timeoutSec, err := GetQueryInfo(ctx, tCtx)
	if err != nil {
		return remote.ResourceMeta{}, err
	}

	clusterPrimaryLabel := getClusterPrimaryLabel(ctx, tCtx, clusterLabelOverride)

	cmdDetails, err := q.client.ExecuteHiveCommand(ctx, query, timeoutSec,
		clusterPrimaryLabel, q.apiKey, tags)
	if err != nil {
		return remote.ResourceMeta{}, err
	}

	// If we succeed, then store the command id returned from Qubole, and update our state. Also, add to the
	// AutoRefreshCache so we start getting updates.
	commandID := strconv.FormatInt(cmdDetails.ID, 10)
	logger.Infof(ctx, "Created Qubole ID [%s]", commandID)

	return remote.ResourceMeta{
		Name: commandID,
	}, nil
}

func (q QuboleHivePlugin) Get(ctx context.Context, key remote.ResourceMeta) (
	resource remote.ResourceMeta, err error) {

	logger.Debugf(ctx, "Retrieving Hive job [%s]", key.Name)

	// Get an updated status from Qubole
	commandStatus, err := q.client.GetCommandStatus(ctx, key.Name, q.apiKey)
	if err != nil {
		logger.Errorf(ctx, "Error from Qubole command %s. Error: %v", key.Name, err)
		return remote.ResourceMeta{}, err
	}

	newExecutionPhase, err := QuboleStatusToExecutionPhase(commandStatus)
	if err != nil {
		return remote.ResourceMeta{}, err
	}

	return remote.ResourceMeta{
		Custom: Resource{
			Phase: newExecutionPhase,
		},
	}, nil
}

func (q QuboleHivePlugin) Delete(ctx context.Context, key remote.ResourceMeta) error {
	err := q.client.KillCommand(ctx, key.Name, q.apiKey)
	if err != nil {
		logger.Errorf(ctx, "Error terminating Qubole command [%s]. Error: %v", key.Name, err)
		return err
	}

	return nil
}

func (q QuboleHivePlugin) Status(_ context.Context, resource remote.ResourceMeta) (
	phase core.PhaseInfo, err error) {

	r, casted := resource.Custom.(Resource)
	if !casted {
		return core.PhaseInfo{}, fmt.Errorf("failed to cast resource to the expected type. Input type: %v",
			reflect.TypeOf(resource))
	}

	return r.GetPhaseInfo(), nil
}

func QuboleHivePluginLoader(ctx context.Context, iCtx remote.PluginSetupContext) (
	remote.Plugin, error) {

	cfg := config.GetQuboleConfig()
	apiKey, err := iCtx.SecretManager().Get(ctx, cfg.TokenKey)
	if err != nil {
		return nil, errors.Wrapf(errors.RuntimeFailure, err, "Failed to read token from secrets manager")
	}

	return QuboleHivePlugin{
		client: client.NewQuboleClient(cfg),
		apiKey: apiKey,
		properties: remote.PluginProperties{
			ResourceQuotas:   BuildResourceConfig(cfg.ClusterConfigs),
			ReadRateLimiter:  cfg.ReadRateLimiter,
			WriteRateLimiter: cfg.WriteRateLimiter,
			Caching:          cfg.Caching,
			CustomState:      Resource{},
		},
	}, nil
}

func init() {
	pluginMachinery.PluginRegistry().RegisterRemotePlugin(
		remote.PluginEntry{
			ID:                 quboleHiveExecutorID,
			SupportedTaskTypes: []core.TaskType{hiveTaskType},
			PluginLoader:       QuboleHivePluginLoader,
		})
}
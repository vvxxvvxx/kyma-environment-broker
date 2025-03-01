package provisioner

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	schema "github.com/kyma-project/control-plane/components/provisioner/pkg/gqlschema"
	"github.com/kyma-project/kyma-environment-broker/common/gardener"
	"github.com/kyma-project/kyma-environment-broker/internal"
	"github.com/kyma-project/kyma-environment-broker/internal/ptr"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

type runtime struct {
	runtimeInput schema.ProvisionRuntimeInput
}

type FakeClient struct {
	mu          sync.Mutex
	graphqlizer Graphqlizer

	runtimes      []runtime
	upgrades      map[string]schema.UpgradeRuntimeInput
	shootUpgrades map[string]schema.UpgradeShootInput
	operations    map[string]schema.OperationStatus
	dumpRequest   bool

	gardenerClient    dynamic.Interface
	gardenerNamespace string
}

func NewFakeClient() *FakeClient {
	return NewFakeClientWithGardener(nil, "")
}

func NewFakeClientWithGardener(gc dynamic.Interface, ns string) *FakeClient {
	return &FakeClient{
		graphqlizer:    Graphqlizer{},
		runtimes:       []runtime{},
		operations:     make(map[string]schema.OperationStatus),
		upgrades:       make(map[string]schema.UpgradeRuntimeInput),
		shootUpgrades:  make(map[string]schema.UpgradeShootInput),
		gardenerClient: gc,
	}
}

func (c *FakeClient) EnableRequestDumping() {
	c.dumpRequest = true
}

func (c *FakeClient) GetLatestProvisionRuntimeInput() schema.ProvisionRuntimeInput {
	c.mu.Lock()
	defer c.mu.Unlock()

	r := c.runtimes[len(c.runtimes)-1]
	return r.runtimeInput
}

func (c *FakeClient) FinishProvisionerOperation(id string, state schema.OperationState) {
	c.mu.Lock()
	defer c.mu.Unlock()

	op := c.operations[id]
	op.State = state
	c.operations[id] = op
}

func (c *FakeClient) FindOperationByRuntimeIDAndType(runtimeID string, operationType schema.OperationType) schema.OperationStatus {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, status := range c.operations {
		if *status.RuntimeID == runtimeID && status.Operation == operationType {
			return status
		}
	}
	return schema.OperationStatus{}
}

func (c *FakeClient) FindOperationByProvisionerOperationID(provisionerOperationID string) schema.OperationStatus {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, status := range c.operations {
		if key == provisionerOperationID {
			return status
		}
	}
	return schema.OperationStatus{}
}

func (c *FakeClient) SetOperation(id string, operation schema.OperationStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.operations[id] = operation
}

// Provisioner Client methods

func (c *FakeClient) ProvisionRuntime(accountID, subAccountID string, config schema.ProvisionRuntimeInput) (schema.OperationStatus, error) {
	rid := uuid.New().String()
	opId := uuid.New().String()

	return c.ProvisionRuntimeWithIDs(accountID, subAccountID, rid, opId, config)
}

func (c *FakeClient) Provision(operation internal.ProvisioningOperation) (schema.OperationStatus, error) {
	input, err := operation.InputCreator.CreateProvisionClusterInput()

	if err != nil {
		return schema.OperationStatus{}, err
	}

	return c.ProvisionRuntimeWithIDs(operation.GlobalAccountID, operation.SubAccountID, operation.RuntimeID, operation.ID, input)
}

func (c *FakeClient) ProvisionRuntimeWithIDs(accountID, subAccountID, runtimeID, operationID string, config schema.ProvisionRuntimeInput) (schema.OperationStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.dumpRequest {
		gql, _ := c.graphqlizer.ProvisionRuntimeInputToGraphQL(config)
		fmt.Println(gql)
	}

	c.runtimes = append(c.runtimes, runtime{
		runtimeInput: config,
	})
	c.operations[operationID] = schema.OperationStatus{
		ID:        &operationID,
		RuntimeID: &runtimeID,
		Operation: schema.OperationTypeProvision,
		State:     schema.OperationStateInProgress,
	}

	if c.gardenerClient != nil {
		shoot := unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "core.gardener.cloud/v1beta1",
				"kind":       "Shoot",
				"metadata": map[string]interface{}{
					"name": config.ClusterConfig.GardenerConfig.Name,
					"annotations": map[string]interface{}{
						"kcp.provisioner.kyma-project.io/runtime-id": runtimeID,
					},
				},
				"spec": map[string]interface{}{
					"maintenance": map[string]interface{}{
						"timeWindow": map[string]interface{}{
							"begin": "010000+0000",
							"end":   "010000+0000",
						},
					},
				},
			},
		}
		c.gardenerClient.Resource(gardener.ShootResource).Namespace(c.gardenerNamespace).Create(context.Background(), &shoot, v1.CreateOptions{})
	}

	return schema.OperationStatus{
		RuntimeID: &runtimeID,
		ID:        &operationID,
	}, nil
}

func (c *FakeClient) DeprovisionRuntime(accountID, runtimeID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	opId := uuid.New().String()

	c.operations[opId] = schema.OperationStatus{
		ID:        &opId,
		Operation: schema.OperationTypeDeprovision,
		State:     schema.OperationStateInProgress,
		RuntimeID: &runtimeID,
	}

	return opId, nil
}

func (c *FakeClient) ReconnectRuntimeAgent(accountID, runtimeID string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

func (c *FakeClient) RuntimeOperationStatus(accountID, operationID string) (schema.OperationStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	o, found := c.operations[operationID]
	if !found {
		return schema.OperationStatus{}, fmt.Errorf("operation not found")
	}
	return o, nil
}

func (c *FakeClient) RuntimeStatus(accountID, runtimeID string) (schema.RuntimeStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, ops := range c.operations {
		if *ops.RuntimeID == runtimeID {
			return schema.RuntimeStatus{
				RuntimeConfiguration: &schema.RuntimeConfig{
					ClusterConfig: &schema.GardenerConfig{
						Name:   ptr.String("fake-name"),
						Region: ptr.String("fake-region"),
						Seed:   ptr.String("fake-seed"),
					},
					Kubeconfig: ptr.String("kubeconfig-content"),
				},
			}, nil
		}
	}

	return schema.RuntimeStatus{}, errors.New("no status for given runtime id")
}

func (c *FakeClient) UpgradeRuntime(accountID, runtimeID string, config schema.UpgradeRuntimeInput) (schema.OperationStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.dumpRequest {
		gql, _ := c.graphqlizer.UpgradeRuntimeInputToGraphQL(config)
		fmt.Println(gql)
	}

	opId := uuid.New().String()
	c.operations[opId] = schema.OperationStatus{
		ID:        &opId,
		RuntimeID: &runtimeID,
		Operation: schema.OperationTypeUpgrade,
		State:     schema.OperationStateInProgress,
	}
	c.upgrades[runtimeID] = config
	return schema.OperationStatus{
		RuntimeID: &runtimeID,
		ID:        &opId,
	}, nil
}

func (c *FakeClient) UpgradeShoot(accountID, runtimeID string, config schema.UpgradeShootInput) (schema.OperationStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.dumpRequest {
		upgradeShootIptGQL, _ := c.graphqlizer.UpgradeShootInputToGraphQL(config)
		fmt.Println(upgradeShootIptGQL)
	}

	opId := uuid.New().String()
	c.operations[opId] = schema.OperationStatus{
		ID:        &opId,
		RuntimeID: &runtimeID,
		Operation: schema.OperationTypeUpgradeShoot,
		State:     schema.OperationStateInProgress,
	}
	c.shootUpgrades[runtimeID] = config
	return schema.OperationStatus{
		RuntimeID: &runtimeID,
		ID:        &opId,
	}, nil
}

func (c *FakeClient) IsRuntimeUpgraded(runtimeID string, version string) bool {
	input, found := c.upgrades[runtimeID]
	if found && version != "" && input.KymaConfig != nil {
		return input.KymaConfig.Version == version
	}

	return found
}

func (c *FakeClient) IsShootUpgraded(runtimeID string) bool {
	_, found := c.shootUpgrades[runtimeID]
	return found
}

func (c *FakeClient) LastShootUpgrade(runtimeID string) (schema.UpgradeShootInput, bool) {
	input, found := c.shootUpgrades[runtimeID]
	return input, found
}

func (c *FakeClient) LastProvisioning() schema.ProvisionRuntimeInput {
	r := c.runtimes[len(c.runtimes)-1]
	return r.runtimeInput
}

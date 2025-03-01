package provisioning

import (
	"fmt"
	"time"

	"github.com/kyma-project/control-plane/components/provisioner/pkg/gqlschema"
	"github.com/kyma-project/kyma-environment-broker/internal"
	kebError "github.com/kyma-project/kyma-environment-broker/internal/error"
	"github.com/kyma-project/kyma-environment-broker/internal/process"
	"github.com/kyma-project/kyma-environment-broker/internal/provisioner"
	"github.com/kyma-project/kyma-environment-broker/internal/storage"
	"github.com/kyma-project/kyma-environment-broker/internal/storage/dberr"
	"github.com/sirupsen/logrus"
)

const (
	// the time after which the operation is marked as expired
	CreateRuntimeTimeout = 1 * time.Hour

	brokerKeyPrefix = "broker_"
	globalKeyPrefix = "global_"
)

type CreateRuntimeWithoutKymaStep struct {
	operationManager    *process.OperationManager
	instanceStorage     storage.Instances
	runtimeStateStorage storage.RuntimeStates
	provisionerClient   provisioner.Client
}

func NewCreateRuntimeWithoutKymaStep(os storage.Operations, runtimeStorage storage.RuntimeStates, is storage.Instances, cli provisioner.Client) *CreateRuntimeWithoutKymaStep {
	return &CreateRuntimeWithoutKymaStep{
		operationManager:    process.NewOperationManager(os),
		instanceStorage:     is,
		provisionerClient:   cli,
		runtimeStateStorage: runtimeStorage,
	}
}

func (s *CreateRuntimeWithoutKymaStep) Name() string {
	return "Create_Runtime_Without_Kyma"
}

func (s *CreateRuntimeWithoutKymaStep) Run(operation internal.Operation, log logrus.FieldLogger) (internal.Operation, time.Duration, error) {
	if operation.RuntimeID != "" {
		log.Infof("RuntimeID already set %s, skipping", operation.RuntimeID)
		return operation, 0, nil
	}
	if time.Since(operation.UpdatedAt) > CreateRuntimeTimeout {
		log.Infof("operation has reached the time limit: updated operation time: %s", operation.UpdatedAt)
		return s.operationManager.OperationFailed(operation, fmt.Sprintf("operation has reached the time limit: %s", CreateRuntimeTimeout), nil, log)
	}

	requestInput, err := s.createProvisionInput(operation)
	if err != nil {
		log.Errorf("Unable to create provisioning input: %s", err.Error())
		return s.operationManager.OperationFailed(operation, "invalid operation data - cannot create provisioning input", err, log)
	}

	log.Infof("call ProvisionRuntime: kubernetesVersion=%s, region=%s, provider=%s, name=%s, workers=%s, pods=%s, services=%s",
		requestInput.ClusterConfig.GardenerConfig.KubernetesVersion,
		requestInput.ClusterConfig.GardenerConfig.Region,
		requestInput.ClusterConfig.GardenerConfig.Provider,
		requestInput.ClusterConfig.GardenerConfig.Name,
		requestInput.ClusterConfig.GardenerConfig.WorkerCidr,
		valueOfString(requestInput.ClusterConfig.GardenerConfig.PodsCidr),
		valueOfString(requestInput.ClusterConfig.GardenerConfig.ServicesCidr))

	provisionerResponse, err := s.provisionerClient.ProvisionRuntime(operation.ProvisioningParameters.ErsContext.GlobalAccountID, operation.ProvisioningParameters.ErsContext.SubAccountID, requestInput)
	switch {
	case kebError.IsTemporaryError(err):
		log.Errorf("call to provisioner failed (temporary error): %s", err)
		return operation, 5 * time.Second, nil
	case err != nil:
		log.Errorf("call to Provisioner failed: %s", err)
		return s.operationManager.OperationFailed(operation, "call to the provisioner service failed", err, log)
	}
	log.Infof("Provisioning runtime in the Provisioner started, RuntimeID=%s, provisioner operation=%s", *provisionerResponse.RuntimeID, *provisionerResponse.ID)

	repeat := time.Duration(0)
	operation, repeat, _ = s.operationManager.UpdateOperation(operation, func(operation *internal.Operation) {
		operation.ProvisionerOperationID = *provisionerResponse.ID
		if provisionerResponse.RuntimeID != nil {
			operation.RuntimeID = *provisionerResponse.RuntimeID
		}
	}, log)
	if repeat != 0 {
		log.Errorf("cannot save operation ID from provisioner")
		return operation, 5 * time.Second, nil
	}

	rs := internal.NewRuntimeState(*provisionerResponse.RuntimeID, operation.ID, requestInput.KymaConfig, requestInput.ClusterConfig.GardenerConfig)
	rs.KymaVersion = operation.RuntimeVersion.Version
	err = s.runtimeStateStorage.Insert(rs)

	if err != nil {
		log.Errorf("cannot insert runtimeState: %s", err)
		return operation, 10 * time.Second, nil
	}

	err = s.updateInstance(operation.InstanceID,
		*provisionerResponse.RuntimeID,
		requestInput.ClusterConfig.GardenerConfig.Region)

	switch {
	case err == nil:
	case dberr.IsConflict(err):
		err := s.updateInstance(operation.InstanceID, *provisionerResponse.RuntimeID, requestInput.ClusterConfig.GardenerConfig.Region)
		if err != nil {
			log.Errorf("cannot update instance: %s", err)
			return operation, 1 * time.Minute, nil
		}
	}

	log.Info("runtime creation process initiated successfully")
	return operation, 0, nil
}

func valueOfString(val *string) string {
	if val == nil {
		return "<nil>"
	}
	return *val
}

func (s *CreateRuntimeWithoutKymaStep) updateInstance(id, runtimeID, region string) error {
	instance, err := s.instanceStorage.GetByID(id)
	if err != nil {
		return fmt.Errorf("while getting instance: %w", err)
	}
	instance.RuntimeID = runtimeID
	instance.ProviderRegion = region
	_, err = s.instanceStorage.Update(*instance)
	if err != nil {
		return fmt.Errorf("while updating instance: %w", err)
	}

	return nil
}

func (s *CreateRuntimeWithoutKymaStep) createProvisionInput(operation internal.Operation) (gqlschema.ProvisionRuntimeInput, error) {
	operation.InputCreator.SetProvisioningParameters(operation.ProvisioningParameters)
	operation.InputCreator.SetShootName(operation.ShootName)
	operation.InputCreator.SetShootDomain(operation.ShootDomain)
	operation.InputCreator.SetShootDNSProviders(operation.ShootDNSProviders)
	operation.InputCreator.SetLabel(brokerKeyPrefix+"instance_id", operation.InstanceID)
	operation.InputCreator.SetLabel(globalKeyPrefix+"subaccount_id", operation.ProvisioningParameters.ErsContext.SubAccountID)
	operation.InputCreator.SetLabel(grafanaURLLabel, fmt.Sprintf("https://grafana.%s", operation.ShootDomain))
	request, err := operation.InputCreator.CreateProvisionClusterInput()
	if err != nil {
		return request, fmt.Errorf("while building input for provisioner: %w", err)
	}
	request.ClusterConfig.GardenerConfig.ShootNetworkingFilterDisabled = operation.ProvisioningParameters.ErsContext.DisableEnterprisePolicyFilter()
	request.ClusterConfig.GardenerConfig.EuAccess = &operation.InstanceDetails.EuAccess

	return request, nil
}

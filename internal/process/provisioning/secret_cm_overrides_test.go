package provisioning

import (
	"testing"
	"time"

	"github.com/kyma-project/kyma-environment-broker/internal"
	"github.com/kyma-project/kyma-environment-broker/internal/process/provisioning/automock"
	"github.com/kyma-project/kyma-environment-broker/internal/storage"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestOverridesFromSecretsAndConfigStep_Run_WithVersionComputed(t *testing.T) {
	t.Run("success run", func(t *testing.T) {
		// Given
		planName := "gcp"
		kymaVersion := "1.15.0"

		memoryStorage := storage.NewMemoryStorage()

		inputCreatorMock := &automock.ProvisionerInputCreator{}
		defer inputCreatorMock.AssertExpectations(t)

		runtimeOverridesMock := &automock.RuntimeOverridesAppender{}
		defer runtimeOverridesMock.AssertExpectations(t)
		runtimeOverridesMock.On("Append", inputCreatorMock, planName, kymaVersion).Return(nil).Once()

		operation := internal.Operation{
			ProvisioningParameters: internal.ProvisioningParameters{
				PlanID:     "ca6e5357-707f-4565-bbbd-b3ab732597c6",
				Parameters: internal.ProvisioningParametersDTO{KymaVersion: kymaVersion}},
			InputCreator: inputCreatorMock,
			Type:         internal.OperationTypeProvision,
		}

		rcvMock := &automock.RuntimeVersionConfiguratorForProvisioning{}
		defer rcvMock.AssertExpectations(t)
		rcvMock.On("ForProvisioning", mock.Anything, mock.Anything).Return(&internal.RuntimeVersionData{Version: kymaVersion}, nil).Once()

		step := NewOverridesFromSecretsAndConfigStep(memoryStorage.Operations(), runtimeOverridesMock, rcvMock)

		// When
		operation, repeat, err := step.Run(operation, logrus.New())

		// Then
		assert.NoError(t, err)
		assert.Equal(t, time.Duration(0), repeat)
	})
}

func TestOverridesFromSecretsAndConfigStep_Run_WithVersionFromOperation(t *testing.T) {
	t.Run("success run", func(t *testing.T) {
		// Given
		planName := "gcp"
		kymaVersion := "1.15.0"

		memoryStorage := storage.NewMemoryStorage()

		inputCreatorMock := &automock.ProvisionerInputCreator{}
		defer inputCreatorMock.AssertExpectations(t)

		runtimeOverridesMock := &automock.RuntimeOverridesAppender{}
		defer runtimeOverridesMock.AssertExpectations(t)
		runtimeOverridesMock.On("Append", inputCreatorMock, planName, kymaVersion).Return(nil).Once()

		operation := internal.Operation{
			ProvisioningParameters: internal.ProvisioningParameters{PlanID: "ca6e5357-707f-4565-bbbd-b3ab732597c6"},
			InputCreator:           inputCreatorMock,
			RuntimeVersion: internal.RuntimeVersionData{
				Version: kymaVersion,
			},
			Type: internal.OperationTypeProvision,
		}

		rcvMock := &automock.RuntimeVersionConfiguratorForProvisioning{}
		defer rcvMock.AssertExpectations(t)

		step := NewOverridesFromSecretsAndConfigStep(memoryStorage.Operations(), runtimeOverridesMock, rcvMock)

		// When
		operation, repeat, err := step.Run(operation, logrus.New())

		// Then
		assert.NoError(t, err)
		assert.Equal(t, time.Duration(0), repeat)
	})
}

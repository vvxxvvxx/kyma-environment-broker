package edp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	kebError "github.com/kyma-project/kyma-environment-broker/internal/error"

	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	MaasConsumerEnvironmentKey = "maasConsumerEnvironment"
	MaasConsumerRegionKey      = "maasConsumerRegion"
	MaasConsumerSubAccountKey  = "maasConsumerSubAccount"
	MaasConsumerServicePlan    = "maasConsumerServicePlan"

	dataTenantTmpl     = "%s/namespaces/%s/dataTenants"
	metadataTenantTmpl = "%s/namespaces/%s/dataTenants/%s/%s/metadata"

	namespaceToken = "%s/oauth2/token"
)

type Config struct {
	AuthURL     string
	AdminURL    string
	Namespace   string
	Secret      string
	Environment string `envconfig:"default=prod"`
	Required    bool   `envconfig:"default=false"`
	Disabled    bool
}

type Client struct {
	config     Config
	httpClient *http.Client
	log        logrus.FieldLogger
}

func NewClient(config Config, log logrus.FieldLogger) *Client {
	cfg := clientcredentials.Config{
		ClientID:     fmt.Sprintf("edp-namespace;%s", config.Namespace),
		ClientSecret: config.Secret,
		TokenURL:     fmt.Sprintf(namespaceToken, config.AuthURL),
		Scopes:       []string{"edp-namespace.read edp-namespace.update"},
	}
	httpClientOAuth := cfg.Client(context.Background())
	httpClientOAuth.Timeout = 30 * time.Second

	return &Client{
		config:     config,
		httpClient: httpClientOAuth,
		log:        log,
	}
}

func (c *Client) dataTenantURL() string {
	return fmt.Sprintf(dataTenantTmpl, c.config.AdminURL, c.config.Namespace)
}

func (c *Client) metadataTenantURL(name, env string) string {
	return fmt.Sprintf(metadataTenantTmpl, c.config.AdminURL, c.config.Namespace, name, env)
}

func (c *Client) CreateDataTenant(data DataTenantPayload) error {
	rawData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("while marshaling dataTenant payload: %w", err)
	}

	return c.post(c.dataTenantURL(), rawData, data.Name)
}

func (c *Client) DeleteDataTenant(name, env string) (err error) {
	URL := fmt.Sprintf("%s/%s/%s", c.dataTenantURL(), name, env)
	request, err := http.NewRequest(http.MethodDelete, URL, nil)
	if err != nil {
		return fmt.Errorf("while creating delete dataTenant request: %w", err)
	}

	response, err := c.httpClient.Do(request)
	defer func() {
		if closeErr := c.closeResponseBody(response); closeErr != nil {
			err = kebError.AsTemporaryError(closeErr, "while closing delete DataTenant response")
		}
	}()
	if err != nil {
		return kebError.AsTemporaryError(err, "while requesting about delete dataTenant")
	}

	return c.processResponse(response, true, name)
}

func (c *Client) CreateMetadataTenant(name, env string, data MetadataTenantPayload) error {
	rawData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("while marshaling tenant metadata payload: %w", err)
	}

	return c.post(c.metadataTenantURL(name, env), rawData, name)
}

func (c *Client) DeleteMetadataTenant(name, env, key string) (err error) {
	URL := fmt.Sprintf("%s/%s", c.metadataTenantURL(name, env), key)
	request, err := http.NewRequest(http.MethodDelete, URL, nil)
	if err != nil {
		return fmt.Errorf("while creating delete metadata request: %w", err)
	}

	response, err := c.httpClient.Do(request)
	defer func() {
		if closeErr := c.closeResponseBody(response); closeErr != nil {
			err = kebError.AsTemporaryError(closeErr, "while closing delete MetadataTenant response")
		}
	}()
	if err != nil {
		return kebError.AsTemporaryError(err, "while requesting about delete metadata")
	}

	return c.processResponse(response, true, name)
}

func (c *Client) GetMetadataTenant(name, env string) (_ []MetadataItem, err error) {
	var metadata []MetadataItem
	request, err := http.NewRequest(http.MethodGet, c.metadataTenantURL(name, env), nil)
	if err != nil {
		return metadata, fmt.Errorf("while creating GET metadata tenant request: %w", err)
	}

	response, err := c.httpClient.Do(request)
	defer func() {
		if closeErr := c.closeResponseBody(response); closeErr != nil {
			err = kebError.AsTemporaryError(closeErr, "while closing get MetadataTenant response")
		}
	}()
	if err != nil {
		return metadata, kebError.AsTemporaryError(err, "while requesting about dataTenant metadata")
	}

	err = json.NewDecoder(response.Body).Decode(&metadata)
	if err != nil {
		return metadata, fmt.Errorf("while decoding dataTenant metadata response: %w", err)
	}

	return metadata, nil
}

func (c *Client) post(URL string, data []byte, id string) (err error) {
	request, err := http.NewRequest(http.MethodPost, URL, bytes.NewBuffer(data))
	if err != nil {
		return fmt.Errorf("while creating POST request for %s: %w", URL, err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	defer func() {
		if closeErr := c.closeResponseBody(response); closeErr != nil {
			err = kebError.AsTemporaryError(closeErr, "while closing post response")
		}
	}()
	if err != nil {
		return kebError.AsTemporaryError(err, "while sending POST request on %s", URL)
	}

	return c.processResponse(response, false, id)
}

func (c *Client) processResponse(response *http.Response, allowNotFound bool, id string) error {
	byteBody, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("while reading response body (status code %d): %w", response.StatusCode, err)
	}
	body := string(byteBody)

	switch response.StatusCode {
	case http.StatusCreated:
		c.log.Infof("Resource created: %s", responseLog(response))
		return nil
	case http.StatusConflict:
		c.log.Warnf("Resource already exist: %s", responseLog(response))
		return NewEDPConflictError(id, "Resource %s already exists", id)
	case http.StatusNoContent:
		c.log.Infof("Action executed correctly: %s", responseLog(response))
		return nil
	case http.StatusNotFound:
		c.log.Infof("Resource not found: %s", responseLog(response))
		if allowNotFound {
			return nil
		}
		c.log.Errorf("Body content: %s", body)
		return NewEDPNotFoundError(id, "Not Found: %s", responseLog(response))
	case http.StatusRequestTimeout:
		c.log.Errorf("Request timeout %s: %s", responseLog(response), body)
		return kebError.WrapNewTemporaryError(NewEDPOtherError(id, http.StatusRequestTimeout, "Request timeout: %s", responseLog(response)))
	case http.StatusBadRequest:
		c.log.Errorf("Bad request %s: %s", responseLog(response), body)
		return NewEDPBadRequestError(id, "Bad request: %s", responseLog(response))
	}

	if response.StatusCode >= 500 {
		c.log.Errorf("EDP server returns failed status %s: %s", responseLog(response), body)
		return kebError.WrapNewTemporaryError(NewEDPOtherError(id, response.StatusCode, "EDP server returns failed status %s", responseLog(response)))
	}

	c.log.Errorf("EDP server not supported response %s: %s", responseLog(response), body)
	return NewEDPOtherError(id, response.StatusCode, "Undefined/empty/notsupported status code response %s", responseLog(response))
}

func responseLog(r *http.Response) string {
	return fmt.Sprintf("Response status code: %d for request %s %s", r.StatusCode, r.Request.Method, r.Request.URL)
}

func (c *Client) closeResponseBody(response *http.Response) error {
	if response == nil {
		return nil
	}
	if response.Body == nil {
		return nil
	}
	return response.Body.Close()
}

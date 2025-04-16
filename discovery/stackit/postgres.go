// Copyright 2020 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package stackit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/common/config"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/discovery/refresh"
	"github.com/prometheus/prometheus/discovery/targetgroup"
	"github.com/stackitcloud/stackit-sdk-go/core/auth"
	stackitconfig "github.com/stackitcloud/stackit-sdk-go/core/config"
)

const (
	stackitPostgresAPIEndpoint         = "https://postgres-flex-service.api.stackit.cloud"
	stackitPostgresPrometheusProxyHost = "postgres-prom-proxy.api.stackit.cloud"
	stackitPostgresRunningState        = "Ready"
)

type postgresFlexDiscovery struct {
	*refresh.Discovery
	httpClient  *http.Client
	logger      *slog.Logger
	apiEndpoint string
	project     string
	region      string
}

func newPostgresFlexDiscovery(conf *SDConfig, logger *slog.Logger) (*postgresFlexDiscovery, error) {
	p := &postgresFlexDiscovery{
		project:     conf.Project,
		region:      conf.Region,
		apiEndpoint: conf.Endpoint,
		logger:      logger,
	}

	rt, err := config.NewRoundTripperFromConfig(conf.HTTPClientConfig, "stackit_sd")
	if err != nil {
		return nil, err
	}

	endpoint := conf.Endpoint
	if endpoint == "" {
		endpoint = stackitPostgresAPIEndpoint
	}

	servers := stackitconfig.ServerConfigurations{}
	noAuth := true
	servers = append(servers, stackitconfig.ServerConfiguration{
		URL:         endpoint,
		Description: "STACKIT PostgresFlex API",
	})

	// If service account key and private key are set, use SDK authentication.
	if conf.ServiceAccountKey != "" || conf.ServiceAccountKeyPath != "" {
		noAuth = false
	}

	p.httpClient = &http.Client{
		Timeout:   time.Duration(conf.RefreshInterval),
		Transport: rt,
	}

	stackitConfiguration := &stackitconfig.Configuration{
		UserAgent:  userAgent,
		HTTPClient: p.httpClient,
		Servers:    servers,
		NoAuth:     noAuth,

		ServiceAccountKey:     conf.ServiceAccountKey,
		PrivateKey:            conf.PrivateKey,
		ServiceAccountKeyPath: conf.ServiceAccountKeyPath,
		PrivateKeyPath:        conf.PrivateKeyPath,
		CredentialsFilePath:   conf.CredentialsFilePath,
	}

	if conf.tokenURL != "" {
		stackitConfiguration.TokenCustomUrl = conf.tokenURL
	}

	authRoundTripper, err := auth.SetupAuth(stackitConfiguration)
	if err != nil {
		return nil, fmt.Errorf("setting up authentication: %w", err)
	}

	p.httpClient.Transport = authRoundTripper
	p.apiEndpoint = strings.TrimSuffix(stackitConfiguration.Servers[0].URL, "/")

	return p, nil
}

func (p *postgresFlexDiscovery) refresh(ctx context.Context) ([]*targetgroup.Group, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		fmt.Sprintf("%s/v2/projects/%s/regions/%s/instances", p.apiEndpoint, p.project, p.region),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	res, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}

	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		errorMessage, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("unexpected status code: %d, message: %s", res.StatusCode, string(errorMessage))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	var postgresFlexListResponse *PostgresFlexListResponse

	if err := json.Unmarshal(body, &postgresFlexListResponse); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if postgresFlexListResponse == nil || postgresFlexListResponse.Items == nil || len(*postgresFlexListResponse.Items) == 0 {
		return []*targetgroup.Group{{Source: "stackit", Targets: []model.LabelSet{}}}, nil
	}

	targets := make([]model.LabelSet, 0, len(*postgresFlexListResponse.Items))
	for _, instance := range *postgresFlexListResponse.Items {
		if instance.Status != stackitPostgresRunningState {
			continue
		}

		labels := model.LabelSet{
			stackitLabelRole:               model.LabelValue(RolePostgresFlex),
			stackitLabelProject:            model.LabelValue(p.project),
			stackitLabelPostgresFlexID:     model.LabelValue(instance.Id),
			stackitLabelPostgresFlexName:   model.LabelValue(instance.Name),
			stackitLabelPostgresFlexStatus: model.LabelValue(instance.Status),
			stackitLabelPostgresFlexRegion: model.LabelValue(p.region),
			model.InstanceLabel:            model.LabelValue(instance.Id),
			model.SchemeLabel:              model.LabelValue("https"),
			model.MetricsPathLabel:         model.LabelValue(fmt.Sprintf("/v2/projects/%s/regions/%s/instances/%s/metrics", p.project, p.region, instance.Id)),
			model.AddressLabel:             stackitPostgresPrometheusProxyHost,
		}

		targets = append(targets, labels)
	}

	return []*targetgroup.Group{{Source: "stackit", Targets: targets}}, nil
}

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
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/discovery/refresh"
	"github.com/prometheus/prometheus/discovery/targetgroup"
	"io"
	"log/slog"
	"net/http"
)

const (
	stackitPostgresAPIEndpoint         = "https://postgres-flex-service.api.stackit.cloud"
	stackitPostgresPrometheusProxyHost = "postgres-prom-proxy.api.stackit.cloud"
	stackitPostgresRunningState        = "Ready"
	stackitLabelPostgresFlexID         = stackitLabelPrefix + "postgres_flex_id"
	stackitLabelPostgresFlexName       = stackitLabelPrefix + "postgres_flex_name"
	stackitLabelPostgresFlexStatus     = stackitLabelPrefix + "postgres_flex_status"
	stackitLabelPostgresFlexRegion     = stackitLabelPrefix + "postgres_flex_region"
)

type postgresFlexDiscovery struct {
	*refresh.Discovery
	httpClient  *http.Client
	logger      *slog.Logger
	apiEndpoint string
	project     string
	region      string
}

// newPostgresFlexDiscovery creates a new Postgres Flex service discovery instance.
// It sets up the HTTP client, authentication, and endpoint configuration using shared logic.
func newPostgresFlexDiscovery(conf *SDConfig, logger *slog.Logger) (*postgresFlexDiscovery, error) {
	base, err := setupDiscoveryBase(
		conf,
		"STACKIT PostgresFlex API",
		func(_ *SDConfig) string {
			return stackitPostgresAPIEndpoint
		},
	)
	if err != nil {
		return nil, err
	}

	return &postgresFlexDiscovery{
		project:     conf.Project,
		region:      conf.Region,
		apiEndpoint: base.apiEndpoint,
		httpClient:  base.httpClient,
		logger:      logger,
	}, nil
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

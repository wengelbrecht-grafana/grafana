package azuremonitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/grafana/grafana/pkg/api/pluginproxy"
	"github.com/grafana/grafana/pkg/components/simplejson"
	"github.com/grafana/grafana/pkg/models"
	"github.com/grafana/grafana/pkg/plugins"
	"github.com/grafana/grafana/pkg/setting"
	"github.com/grafana/grafana/pkg/util/errutil"
	"github.com/opentracing/opentracing-go"
	"golang.org/x/net/context/ctxhttp"
)

// ApplicationInsightsDatasource calls the application insights query API.
type ApplicationInsightsDatasource struct {
	httpClient    *http.Client
	dsInfo        *models.DataSource
	pluginManager plugins.Manager
	cfg           *setting.Cfg
}

// ApplicationInsightsQuery is the model that holds the information
// needed to make a metrics query to Application Insights, and the information
// used to parse the response.
type ApplicationInsightsQuery struct {
	RefID string

	// Text based raw query options.
	ApiURL string
	Params url.Values
	Alias  string
	Target string

	// These fields are used when parsing the response.
	metricName  string
	dimensions  []string
	aggregation string
}

// nolint:staticcheck // plugins.DataQueryResult deprecated
func (e *ApplicationInsightsDatasource) executeTimeSeriesQuery(ctx context.Context,
	originalQueries []plugins.DataSubQuery,
	timeRange plugins.DataTimeRange) (plugins.DataResponse, error) {
	result := plugins.DataResponse{
		Results: map[string]plugins.DataQueryResult{},
	}

	queries, err := e.buildQueries(originalQueries, timeRange)
	if err != nil {
		return plugins.DataResponse{}, err
	}

	for _, query := range queries {
		queryRes, err := e.executeQuery(ctx, query)
		if err != nil {
			return plugins.DataResponse{}, err
		}
		result.Results[query.RefID] = queryRes
	}

	return result, nil
}

func (e *ApplicationInsightsDatasource) buildQueries(queries []plugins.DataSubQuery,
	timeRange plugins.DataTimeRange) ([]*ApplicationInsightsQuery, error) {
	applicationInsightsQueries := []*ApplicationInsightsQuery{}
	startTime, err := timeRange.ParseFrom()
	if err != nil {
		return nil, err
	}

	endTime, err := timeRange.ParseTo()
	if err != nil {
		return nil, err
	}

	for _, query := range queries {
		queryBytes, err := query.Model.Encode()
		if err != nil {
			return nil, fmt.Errorf("failed to re-encode the Azure Application Insights query into JSON: %w", err)
		}
		queryJSONModel := insightsJSONQuery{}
		err = json.Unmarshal(queryBytes, &queryJSONModel)
		if err != nil {
			return nil, fmt.Errorf("failed to decode the Azure Application Insights query object from JSON: %w", err)
		}

		insightsJSONModel := queryJSONModel.AppInsights
		azlog.Debug("Application Insights", "target", insightsJSONModel)

		azureURL := fmt.Sprintf("metrics/%s", insightsJSONModel.MetricName)
		timeGrain := insightsJSONModel.TimeGrain
		timeGrains := insightsJSONModel.AllowedTimeGrainsMs

		// Previous versions of the query model don't specify a time grain, so we
		// need to fallback to a default value
		if timeGrain == "auto" || timeGrain == "" {
			timeGrain, err = setAutoTimeGrain(query.IntervalMS, timeGrains)
			if err != nil {
				return nil, err
			}
		}

		params := url.Values{}
		params.Add("timespan", fmt.Sprintf("%v/%v", startTime.UTC().Format(time.RFC3339), endTime.UTC().Format(time.RFC3339)))
		if timeGrain != "none" {
			params.Add("interval", timeGrain)
		}
		params.Add("aggregation", insightsJSONModel.Aggregation)

		dimensionFilter := strings.TrimSpace(insightsJSONModel.DimensionFilter)
		if dimensionFilter != "" {
			params.Add("filter", dimensionFilter)
		}

		if len(insightsJSONModel.Dimensions) != 0 {
			params.Add("segment", strings.Join(insightsJSONModel.Dimensions, ","))
		}
		applicationInsightsQueries = append(applicationInsightsQueries, &ApplicationInsightsQuery{
			RefID:       query.RefID,
			ApiURL:      azureURL,
			Params:      params,
			Alias:       insightsJSONModel.Alias,
			Target:      params.Encode(),
			metricName:  insightsJSONModel.MetricName,
			aggregation: insightsJSONModel.Aggregation,
			dimensions:  insightsJSONModel.Dimensions,
		})
	}

	return applicationInsightsQueries, nil
}

// nolint:staticcheck // plugins.DataQueryResult deprecated
func (e *ApplicationInsightsDatasource) executeQuery(ctx context.Context, query *ApplicationInsightsQuery) (
	plugins.DataQueryResult, error) {
	queryResult := plugins.DataQueryResult{Meta: simplejson.New(), RefID: query.RefID}

	req, err := e.createRequest(ctx, e.dsInfo)
	if err != nil {
		queryResult.Error = err
		return queryResult, nil
	}

	req.URL.Path = path.Join(req.URL.Path, query.ApiURL)
	req.URL.RawQuery = query.Params.Encode()

	span, ctx := opentracing.StartSpanFromContext(ctx, "application insights query")
	span.SetTag("target", query.Target)
	span.SetTag("datasource_id", e.dsInfo.Id)
	span.SetTag("org_id", e.dsInfo.OrgId)

	defer span.Finish()

	err = opentracing.GlobalTracer().Inject(
		span.Context(),
		opentracing.HTTPHeaders,
		opentracing.HTTPHeadersCarrier(req.Header))

	if err != nil {
		azlog.Warn("failed to inject global tracer")
	}

	azlog.Debug("ApplicationInsights", "Request URL", req.URL.String())
	res, err := ctxhttp.Do(ctx, e.httpClient, req)
	if err != nil {
		queryResult.Error = err
		return queryResult, nil
	}

	body, err := ioutil.ReadAll(res.Body)
	defer func() {
		if err := res.Body.Close(); err != nil {
			azlog.Warn("Failed to close response body", "err", err)
		}
	}()
	if err != nil {
		return plugins.DataQueryResult{}, err
	}

	if res.StatusCode/100 != 2 {
		azlog.Debug("Request failed", "status", res.Status, "body", string(body))
		return plugins.DataQueryResult{}, fmt.Errorf("request failed, status: %s", res.Status)
	}

	mr := MetricsResult{}
	err = json.Unmarshal(body, &mr)
	if err != nil {
		return plugins.DataQueryResult{}, err
	}

	frame, err := InsightsMetricsResultToFrame(mr, query.metricName, query.aggregation, query.dimensions)
	if err != nil {
		queryResult.Error = err
		return queryResult, nil
	}

	applyInsightsMetricAlias(frame, query.Alias)

	queryResult.Dataframes = plugins.NewDecodedDataFrames(data.Frames{frame})
	return queryResult, nil
}

func (e *ApplicationInsightsDatasource) createRequest(ctx context.Context, dsInfo *models.DataSource) (*http.Request, error) {
	// find plugin
	plugin := e.pluginManager.GetDataSource(dsInfo.Type)
	if plugin == nil {
		return nil, errors.New("unable to find datasource plugin Azure Application Insights")
	}

	appInsightsRoute, routeName, err := e.getPluginRoute(plugin)
	if err != nil {
		return nil, err
	}

	appInsightsAppID := dsInfo.JsonData.Get("appInsightsAppId").MustString()
	proxyPass := fmt.Sprintf("%s/v1/apps/%s", routeName, appInsightsAppID)

	u, err := url.Parse(dsInfo.Url)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, fmt.Sprintf("/v1/apps/%s", appInsightsAppID))

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		azlog.Debug("Failed to create request", "error", err)
		return nil, errutil.Wrap("Failed to create request", err)
	}

	pluginproxy.ApplyRoute(ctx, req, proxyPass, appInsightsRoute, dsInfo, e.cfg)

	return req, nil
}

func (e *ApplicationInsightsDatasource) getPluginRoute(plugin *plugins.DataSourcePlugin) (*plugins.AppPluginRoute, string, error) {
	cloud, err := getAzureCloud(e.cfg, e.dsInfo.JsonData)
	if err != nil {
		return nil, "", err
	}

	routeName, err := getAppInsightsApiRoute(cloud)
	if err != nil {
		return nil, "", err
	}

	var pluginRoute *plugins.AppPluginRoute
	for _, route := range plugin.Routes {
		if route.Path == routeName {
			pluginRoute = route
			break
		}
	}

	return pluginRoute, routeName, nil
}

// formatApplicationInsightsLegendKey builds the legend key or timeseries name
// Alias patterns like {{metric}} are replaced with the appropriate data values.
func formatApplicationInsightsLegendKey(alias string, metricName string, labels data.Labels) string {
	// Could be a collision problem if there were two keys that varied only in case, but I don't think that would happen in azure.
	lowerLabels := data.Labels{}
	for k, v := range labels {
		lowerLabels[strings.ToLower(k)] = v
	}
	keys := make([]string, 0, len(labels))
	for k := range lowerLabels {
		keys = append(keys, k)
	}
	keys = sort.StringSlice(keys)

	result := legendKeyFormat.ReplaceAllFunc([]byte(alias), func(in []byte) []byte {
		metaPartName := strings.Replace(string(in), "{{", "", 1)
		metaPartName = strings.Replace(metaPartName, "}}", "", 1)
		metaPartName = strings.ToLower(strings.TrimSpace(metaPartName))

		switch metaPartName {
		case "metric":
			return []byte(metricName)
		case "dimensionname", "groupbyname":
			return []byte(keys[0])
		case "dimensionvalue", "groupbyvalue":
			return []byte(lowerLabels[keys[0]])
		}

		if v, ok := lowerLabels[metaPartName]; ok {
			return []byte(v)
		}

		return in
	})

	return string(result)
}

func applyInsightsMetricAlias(frame *data.Frame, alias string) {
	if alias == "" {
		return
	}

	for _, field := range frame.Fields {
		if field.Type() == data.FieldTypeTime || field.Type() == data.FieldTypeNullableTime {
			continue
		}

		displayName := formatApplicationInsightsLegendKey(alias, field.Name, field.Labels)

		if field.Config == nil {
			field.Config = &data.FieldConfig{}
		}

		field.Config.DisplayName = displayName
	}
}

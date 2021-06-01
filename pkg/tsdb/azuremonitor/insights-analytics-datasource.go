package azuremonitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"

	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/grafana/grafana/pkg/api/pluginproxy"
	"github.com/grafana/grafana/pkg/models"
	"github.com/grafana/grafana/pkg/plugins"
	"github.com/grafana/grafana/pkg/setting"
	"github.com/grafana/grafana/pkg/util/errutil"
	"github.com/opentracing/opentracing-go"
	"golang.org/x/net/context/ctxhttp"
)

type InsightsAnalyticsDatasource struct {
	httpClient    *http.Client
	dsInfo        *models.DataSource
	pluginManager plugins.Manager
	cfg           *setting.Cfg
}

type InsightsAnalyticsQuery struct {
	RefID string

	RawQuery          string
	InterpolatedQuery string

	ResultFormat string

	Params url.Values
	Target string
}

//nolint: staticcheck // plugins.DataPlugin deprecated
func (e *InsightsAnalyticsDatasource) executeTimeSeriesQuery(ctx context.Context,
	originalQueries []plugins.DataSubQuery, timeRange plugins.DataTimeRange) (plugins.DataResponse, error) {
	result := plugins.DataResponse{
		Results: map[string]plugins.DataQueryResult{},
	}

	queries, err := e.buildQueries(originalQueries, timeRange)
	if err != nil {
		return plugins.DataResponse{}, err
	}

	for _, query := range queries {
		result.Results[query.RefID] = e.executeQuery(ctx, query)
	}

	return result, nil
}

func (e *InsightsAnalyticsDatasource) buildQueries(queries []plugins.DataSubQuery,
	timeRange plugins.DataTimeRange) ([]*InsightsAnalyticsQuery, error) {
	iaQueries := []*InsightsAnalyticsQuery{}

	for _, query := range queries {
		queryBytes, err := query.Model.Encode()
		if err != nil {
			return nil, fmt.Errorf("failed to re-encode the Azure Application Insights Analytics query into JSON: %w", err)
		}

		qm := InsightsAnalyticsQuery{}
		queryJSONModel := insightsAnalyticsJSONQuery{}
		err = json.Unmarshal(queryBytes, &queryJSONModel)
		if err != nil {
			return nil, fmt.Errorf("failed to decode the Azure Application Insights Analytics query object from JSON: %w", err)
		}

		qm.RawQuery = queryJSONModel.InsightsAnalytics.Query
		qm.ResultFormat = queryJSONModel.InsightsAnalytics.ResultFormat
		qm.RefID = query.RefID

		if qm.RawQuery == "" {
			return nil, fmt.Errorf("query is missing query string property")
		}

		qm.InterpolatedQuery, err = KqlInterpolate(query, timeRange, qm.RawQuery)
		if err != nil {
			return nil, err
		}
		qm.Params = url.Values{}
		qm.Params.Add("query", qm.InterpolatedQuery)

		qm.Target = qm.Params.Encode()
		iaQueries = append(iaQueries, &qm)
	}

	return iaQueries, nil
}

//nolint: staticcheck // plugins.DataPlugin deprecated
func (e *InsightsAnalyticsDatasource) executeQuery(ctx context.Context, query *InsightsAnalyticsQuery) plugins.DataQueryResult {
	queryResult := plugins.DataQueryResult{RefID: query.RefID}

	queryResultError := func(err error) plugins.DataQueryResult {
		queryResult.Error = err
		return queryResult
	}

	req, err := e.createRequest(ctx, e.dsInfo)
	if err != nil {
		return queryResultError(err)
	}
	req.URL.Path = path.Join(req.URL.Path, "query")
	req.URL.RawQuery = query.Params.Encode()

	span, ctx := opentracing.StartSpanFromContext(ctx, "application insights analytics query")
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
		return queryResultError(err)
	}

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return queryResultError(err)
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			azlog.Warn("Failed to close response body", "err", err)
		}
	}()

	if res.StatusCode/100 != 2 {
		azlog.Debug("Request failed", "status", res.Status, "body", string(body))
		return queryResultError(fmt.Errorf("request failed, status: %s, body: %s", res.Status, body))
	}
	var logResponse AzureLogAnalyticsResponse
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	err = d.Decode(&logResponse)
	if err != nil {
		return queryResultError(err)
	}

	t, err := logResponse.GetPrimaryResultTable()
	if err != nil {
		return queryResultError(err)
	}

	frame, err := ResponseTableToFrame(t)
	if err != nil {
		return queryResultError(err)
	}

	if query.ResultFormat == timeSeries {
		tsSchema := frame.TimeSeriesSchema()
		if tsSchema.Type == data.TimeSeriesTypeLong {
			wideFrame, err := data.LongToWide(frame, nil)
			if err == nil {
				frame = wideFrame
			} else {
				frame.AppendNotices(data.Notice{
					Severity: data.NoticeSeverityWarning,
					Text:     "could not convert frame to time series, returning raw table: " + err.Error(),
				})
			}
		}
	}
	frames := data.Frames{frame}
	queryResult.Dataframes = plugins.NewDecodedDataFrames(frames)

	return queryResult
}

func (e *InsightsAnalyticsDatasource) createRequest(ctx context.Context, dsInfo *models.DataSource) (*http.Request, error) {
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
		return nil, fmt.Errorf("unable to parse url for Application Insights Analytics datasource: %w", err)
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

func (e *InsightsAnalyticsDatasource) getPluginRoute(plugin *plugins.DataSourcePlugin) (*plugins.AppPluginRoute, string, error) {
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

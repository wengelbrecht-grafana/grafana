import {
  DataQuery,
  DataQueryRequest,
  DataQueryResponse,
  DataSourceApi,
  DataSourceInstanceSettings,
} from '@grafana/data';
import { DataSourceWithBackend } from '@grafana/runtime';
import { TraceToLogsData, TraceToLogsOptions } from 'app/core/components/TraceToLogsSettings';
import { getDatasourceSrv } from 'app/features/plugins/datasource_srv';
import { Observable } from 'rxjs';
import { map } from 'rxjs/operators';
import { transformTrace, transformTraceList } from './resultTransformer';

export type TempoQuery = {
  query: string;
  // Query to find list of traces, e.g., via Loki
  linkedQuery?: DataQuery;
} & DataQuery;

export class TempoDatasource extends DataSourceWithBackend<TempoQuery, TraceToLogsData> {
  tracesToLogs: TraceToLogsOptions;
  linkedDatasource: DataSourceApi;
  constructor(instanceSettings: DataSourceInstanceSettings<TraceToLogsData>) {
    super(instanceSettings);
    this.tracesToLogs = instanceSettings.jsonData.tracesToLogs || {};
    if (this.tracesToLogs.datasourceUid) {
      this.linkDatasource();
    }
  }

  async linkDatasource() {
    const dsSrv = getDatasourceSrv();
    this.linkedDatasource = await dsSrv.get(this.tracesToLogs.datasourceUid);
  }

  query(options: DataQueryRequest<TempoQuery>): Observable<DataQueryResponse> {
    // If there is a linked query, run that instead. This is used to provide a list of traces.
    console.log('querying from datasource', options);
    if (options.targets.some((t) => t.linkedQuery) && this.linkedDatasource) {
      // Wrap linked query into a data request
      const linkedQuery = options.targets.find((t) => t.linkedQuery)?.linkedQuery;
      const linkedRequest: DataQueryRequest = { ...options, targets: [linkedQuery!] };
      return (this.linkedDatasource.query(linkedRequest) as Observable<DataQueryResponse>).pipe(
        map((response) => transformTraceList(response, this.linkedDatasource))
      );
    }

    return super.query(options).pipe(
      map((response) => {
        if (response.error) {
          return response;
        }

        return transformTrace(response);
      })
    );
  }

  async testDatasource(): Promise<any> {
    const response = await super.query({ targets: [{ query: '', refId: 'A' }] } as any).toPromise();

    if (!response.error?.message?.startsWith('failed to get trace')) {
      return { status: 'error', message: 'Data source is not working' };
    }

    return { status: 'success', message: 'Data source is working' };
  }

  getQueryDisplayText(query: TempoQuery) {
    return query.query;
  }
}

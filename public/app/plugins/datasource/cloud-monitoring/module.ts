import { DataSourcePlugin } from '@grafana/data';
import CloudMonitoringDatasource from './datasource';
import { QueryEditor } from './components/QueryEditor';
import CloudMonitoringCheatSheet from './components/CloudMonitoringCheatSheet';
import { CloudMonitoringConfigCtrl } from './config_ctrl';
import { CloudMonitoringAnnotationsQueryCtrl } from './annotations_query_ctrl';
import { CloudMonitoringVariableQueryEditor } from './components/VariableQueryEditor';
import { CloudMonitoringQuery } from './types';

export const plugin = new DataSourcePlugin<CloudMonitoringDatasource, CloudMonitoringQuery>(CloudMonitoringDatasource)
  .setQueryEditorHelp(CloudMonitoringCheatSheet)
  .setQueryEditor(QueryEditor)
  .setConfigCtrl(CloudMonitoringConfigCtrl)
  .setAnnotationQueryCtrl(CloudMonitoringAnnotationsQueryCtrl)
  .setVariableQueryEditor(CloudMonitoringVariableQueryEditor);

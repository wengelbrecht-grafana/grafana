import {
  DataQueryResponse,
  ArrayVector,
  DataFrame,
  Field,
  FieldType,
  MutableDataFrame,
  DataSourceApi,
} from '@grafana/data';

const emptyDataQueryResponse = {
  data: [
    new MutableDataFrame({
      fields: [
        {
          name: 'trace',
          type: FieldType.trace,
          values: [],
        },
      ],
      meta: {
        preferredVisualisationType: 'trace',
      },
    }),
  ],
};

export function createTableFrame(logsFrame: DataFrame, datasourceUid: string, datasourceName: string): DataFrame {
  const traceRegex = /traceID=(\w+)/;
  const tableFrame = new MutableDataFrame({
    fields: [
      {
        name: 'traceID',
        type: FieldType.string,
        config: {
          displayNameFromDS: 'Trace ID',
          links: [
            {
              title: 'Trace: ${__value.raw}',
              url: '',
              internal: {
                datasourceUid,
                datasourceName,
                query: {
                  query: '${__value.raw}',
                },
              },
            },
          ],
        },
      },
    ],
    meta: {
      preferredVisualisationType: 'table',
    },
  });

  logsFrame.fields.forEach((field) => {
    if (field.type === FieldType.string) {
      field.values.toArray().forEach((value) => {
        if (value) {
          const match = (value as string).match(traceRegex);
          if (match) {
            const traceId = match[1];
            tableFrame.fields[0].values.add(traceId);
          }
        }
      });
    }
  });

  return tableFrame;
}

export function transformTraceList(response: DataQueryResponse, datasource: DataSourceApi): DataQueryResponse {
  const frame = createTableFrame(response.data[0], datasource.uid, datasource.name);
  response.data[0] = frame;
  return response;
}

export function transformTrace(response: DataQueryResponse): DataQueryResponse {
  // We need to parse some of the fields which contain stringified json.
  // Seems like we can't just map the values as the frame we got from backend has some default processing
  // and will stringify the json back when we try to set it. So we create a new field and swap it instead.
  const frame: DataFrame = response.data[0];

  if (!frame) {
    return emptyDataQueryResponse;
  }

  for (const fieldName of ['serviceTags', 'logs', 'tags']) {
    const field = frame.fields.find((f) => f.name === fieldName);
    if (field) {
      const fieldIndex = frame.fields.indexOf(field);
      const values = new ArrayVector();
      const newField: Field = {
        ...field,
        values,
        type: FieldType.other,
      };

      for (let i = 0; i < field.values.length; i++) {
        const value = field.values.get(i);
        values.set(i, value === '' ? undefined : JSON.parse(value));
      }
      frame.fields[fieldIndex] = newField;
    }
  }

  return response;
}

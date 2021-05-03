import { FieldType, MutableDataFrame } from '@grafana/data';
import { createTableFrame } from './resultTransformer';

/**
 *   const frame = new MutableDataFrame({
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
            },
          ],
        },
      },
      //   { name: 'traceName', type: FieldType.string, config: { displayNameFromDS: 'Trace name' } },
      //   { name: 'startTime', type: FieldType.time, config: { displayNameFromDS: 'Start time' } },
    ],
    meta: {
      preferredVisualisationType: 'table',
    },
  });
 
 */

describe('transformTraceList()', () => {
  const lokiDataFrame = new MutableDataFrame({
    fields: [
      {
        name: 'ts',
        type: FieldType.time,
        values: ['2020-02-12T15:05:14.265Z'],
      },
      {
        name: 'line',
        type: FieldType.string,
        values: ['t=2020-02-12T15:04:51+0000 lvl=info msg="Starting Grafana" logger=server traceID=asdfa1234'],
      },
      {
        name: 'id',
        type: FieldType.string,
        values: ['19e8e093d70122b3b53cb6e24efd6e2d'],
      },
    ],
    meta: {
      preferredVisualisationType: 'table',
    },
  });

  test('extracts traceIDs from log lines', () => {
    const frame = createTableFrame(lokiDataFrame, 't1', 'tempo');
    expect(frame.fields[0].name).toBe('traceID');
    expect(frame.fields[0].values.get(0)).toBe('asdfa1234');
  });
});

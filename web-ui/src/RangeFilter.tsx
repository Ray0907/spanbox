import {HStack} from '@astryxdesign/core/Layout';
import {Selector} from '@astryxdesign/core/Selector';
import {DateTimeInput} from '@astryxdesign/core/DateTimeInput';
import {rangeOptions, fromPickerValue, toPickerValue} from './traces';

// The Range choice shared by Traces, Sessions and Dashboard.
export function RangeSelector({value, onChange, options = rangeOptions}: {value: string; onChange: (value: string) => void; options?: {value: string; label: string}[]}) {
  return <Selector label="Range" options={options} value={value} onChange={onChange} />;
}

// From and To for the custom range, shared by Traces and Sessions. The picker value is read as the UTC wall clock.
export function CustomBounds({from, to, onChange}: {from: string; to: string; onChange: (name: 'from' | 'to', rfc3339: string) => void}) {
  return <HStack gap={2} align="start" wrap="wrap">
    {([['from', 'From', from], ['to', 'To', to]] as const).map(([name, label, value]) => <DateTimeInput key={name} label={`${label} (UTC)`}
      presentation="popover" hourFormat="24h" timeOptionInterval={15} value={toPickerValue(value)} onChange={picked => onChange(name, fromPickerValue(picked))} />)}
  </HStack>;
}

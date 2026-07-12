"use client";

import { DatePicker } from "antd";
import dayjs from "dayjs";

type DateRangeFilterProps = {
  startDate: string;
  endDate: string;
  onChange: (startDate: string, endDate: string) => void;
};

export function DateRangeFilter({ startDate, endDate, onChange }: DateRangeFilterProps) {
  const value = startDate
    ? ([dayjs(startDate), endDate ? dayjs(endDate) : dayjs(startDate)] as [dayjs.Dayjs, dayjs.Dayjs])
    : null;

  return (
    <DatePicker.RangePicker
      allowClear
      className="h-10 w-[260px]"
      format="YYYY-MM-DD"
      placeholder={["开始日期", "结束日期"]}
      value={value}
      onChange={(_, dateStrings) => onChange(dateStrings[0] || "", dateStrings[1] || "")}
    />
  );
}

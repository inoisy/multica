"use client";

import type { ReactNode } from "react";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";

export function SimpleSelect({ items, value, onValueChange, disabled, className, "aria-label": label }: {
  items: { value: string; label: ReactNode }[];
  value: string;
  onValueChange: (value: string) => void;
  disabled?: boolean;
  className?: string;
  "aria-label"?: string;
}) {
  return (
    <Select items={items} value={value} disabled={disabled} onValueChange={(next) => { if (next !== null) onValueChange(next); }}>
      <SelectTrigger className={className} aria-label={label}><SelectValue /></SelectTrigger>
      <SelectContent>{items.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent>
    </Select>
  );
}

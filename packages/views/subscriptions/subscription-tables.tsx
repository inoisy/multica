"use client";

import { Fragment, useState } from "react";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../i18n";

/** Колонка таблицы: text — строка, list — список через запятую, select — одно из `options`. */
export interface TableColumn {
  field: string;
  label: string;
  kind: "text" | "list" | "select";
  options?: { value: string; label: string }[];
  mono?: boolean;
}

type Row = Record<string, unknown>;

function show(value: unknown) {
  if (Array.isArray(value)) return value.join(", ");
  return value === null || value === undefined ? "" : String(value);
}

/**
 * Таблица пульта (подписки, runtime, окна): чтение всем, правка строки — владельцу.
 * Строка сохраняется целиком; ключ задаётся только у новой строки.
 */
export function EditableTable({
  title,
  description,
  keyField,
  keyLabel,
  columns,
  rows,
  canEdit,
  busy,
  onSave,
  onDelete,
}: {
  title: string;
  description: string;
  keyField: string;
  keyLabel: string;
  columns: TableColumn[];
  rows: Row[];
  canEdit: boolean;
  busy: boolean;
  onSave: (key: string, row: Row) => Promise<boolean>;
  onDelete: (key: string) => Promise<boolean>;
}) {
  const { t } = useT("agents");
  const [editing, setEditing] = useState<string | null>(null);
  const [isNew, setIsNew] = useState(false);
  const [draft, setDraft] = useState<Record<string, string>>({});

  const start = (row: Row | null) => {
    setIsNew(row === null);
    setEditing(row === null ? "" : String(row[keyField]));
    setDraft(Object.fromEntries([keyField, ...columns.map((c) => c.field)].map((f) => [f, row ? show(row[f]) : ""])));
  };

  const save = async () => {
    const key = ((isNew ? draft[keyField] : editing) ?? "").trim();
    const row: Row = {};
    for (const column of columns) {
      const value = draft[column.field] ?? "";
      row[column.field] =
        column.kind === "list"
          ? value
              .split(",")
              .map((part) => part.trim())
              .filter(Boolean)
          : value.trim();
    }
    if (await onSave(key, row)) setEditing(null);
  };

  const field = (name: string, column?: TableColumn) =>
    column?.kind === "select" ? (
      <select
        className="h-8 rounded-md border bg-background px-2 text-caption"
        value={draft[name] ?? ""}
        onChange={(event) => setDraft({ ...draft, [name]: event.target.value })}
      >
        {(column.options ?? []).map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
    ) : (
      <Input
        className="h-8 min-w-28 text-caption"
        value={draft[name] ?? ""}
        onChange={(event) => setDraft({ ...draft, [name]: event.target.value })}
      />
    );

  const editRow = (
    <tr className="border-t bg-muted/30 align-top">
      <td className="p-2">{isNew ? field(keyField) : <span className="font-mono">{editing}</span>}</td>
      {columns.map((column) => (
        <td key={column.field} className="p-2">
          {field(column.field, column)}
        </td>
      ))}
      <td className="p-2">
        <div className="flex gap-1">
          <Button size="sm" disabled={busy} onClick={() => void save()}>
            {t(($) => $.subscriptions.save)}
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setEditing(null)}>
            {t(($) => $.subscriptions.cancel)}
          </Button>
        </div>
      </td>
    </tr>
  );

  return (
    <section className="space-y-2">
      <div className="flex items-end justify-between gap-3">
        <div>
          <h2 className="text-body font-medium">{title}</h2>
          <p className="text-caption text-muted-foreground">{description}</p>
        </div>
        {canEdit && editing === null ? (
          <Button size="sm" variant="outline" onClick={() => start(null)}>
            <Plus />
            {t(($) => $.subscriptions.add_row)}
          </Button>
        ) : null}
      </div>
      <div className="overflow-x-auto rounded-lg border">
        <table className="w-full text-caption">
          <thead className="bg-muted/40 text-left text-muted-foreground">
            <tr>
              <th className="p-2 font-medium">{keyLabel}</th>
              {columns.map((column) => (
                <th key={column.field} className="p-2 font-medium">
                  {column.label}
                </th>
              ))}
              <th className="w-px p-2" />
            </tr>
          </thead>
          <tbody>
            {rows.map((row) =>
              editing === String(row[keyField]) && !isNew ? (
                <Fragment key={String(row[keyField])}>{editRow}</Fragment>
              ) : (
                <tr key={String(row[keyField])} className="border-t align-top">
                  <td className="p-2 font-mono">{String(row[keyField])}</td>
                  {columns.map((column) => {
                    const value = show(row[column.field]);
                    const label = column.options?.find((option) => option.value === value)?.label ?? value;
                    return (
                      <td key={column.field} className={column.mono ? "p-2 font-mono" : "p-2"}>
                        {label || "—"}
                      </td>
                    );
                  })}
                  <td className="p-2">
                    {canEdit && editing === null ? (
                      <div className="flex gap-1">
                        <Button
                          size="icon-xs"
                          variant="ghost"
                          aria-label={t(($) => $.subscriptions.edit_row, { key: String(row[keyField]) })}
                          onClick={() => start(row)}
                        >
                          <Pencil />
                        </Button>
                        <Button
                          size="icon-xs"
                          variant="ghost"
                          aria-label={t(($) => $.subscriptions.delete_row, { key: String(row[keyField]) })}
                          disabled={busy}
                          onClick={() => {
                            if (window.confirm(t(($) => $.subscriptions.delete_confirm, { key: String(row[keyField]) }))) {
                              void onDelete(String(row[keyField]));
                            }
                          }}
                        >
                          <Trash2 />
                        </Button>
                      </div>
                    ) : null}
                  </td>
                </tr>
              ),
            )}
            {isNew && editing !== null ? editRow : null}
          </tbody>
        </table>
      </div>
    </section>
  );
}


import {
  createColumnHelper,
  createPaginatedRowModel,
  createSortedRowModel,
  rowPaginationFeature,
  rowSortingFeature,
  sortFns,
  tableFeatures,
  useTable,
  type ColumnDef,
  type PaginationState,
  type RowData,
  type SortingState,
} from "@tanstack/react-table";
import { useEffect, useState, type CSSProperties, type ReactNode } from "react";

import { IconButton } from "./Button";
import { Icon } from "./Icon";

// Column-level rendering hints. v9 reads this from the features object, which
// is the supported alternative to declaration merging.
export type MCPColumnMeta = {
  /** Render this column as the row's <th scope="row">. */
  rowHeader?: boolean;
  /** Right-align and tabular-align a numeric column. */
  numeric?: boolean;
  /** Shared header/body width; opting in uses fixed table layout. */
  width?: CSSProperties["width"];
};

// One feature set for every table in the console, so only sorting and
// pagination ship in the bundle.
export const dataTableFeatures = tableFeatures({
  rowSortingFeature,
  rowPaginationFeature,
  sortedRowModel: createSortedRowModel(),
  paginatedRowModel: createPaginatedRowModel(),
  sortFns,
  columnMeta: {} as MCPColumnMeta,
});

export type DataColumn<T extends RowData> = ColumnDef<typeof dataTableFeatures, T, any>;

export function dataColumnHelper<T extends RowData>() {
  return createColumnHelper<typeof dataTableFeatures, T>();
}

// flexRender turns a cell/header function into a React *component type*. Our
// cell renderers close over component state, so their identity changes on every
// render and React would unmount and remount each cell - losing focus inside a
// row and defeating focus restoration when an inspector closes. Calling the
// renderer directly inlines the nodes into the existing cell instead.
function renderTemplate(template: unknown, context: unknown): ReactNode {
  if (typeof template === "function") {
    return (template as (ctx: unknown) => ReactNode)(context);
  }
  return template as ReactNode;
}

function sortLabel(direction: false | "asc" | "desc"): string {
  if (direction === "asc") return "ascending";
  if (direction === "desc") return "descending";
  return "none";
}

type DataTableProps<T extends RowData> = {
  columns: Array<DataColumn<T>>;
  rows: T[];
  rowKey: (row: T) => string;
  /** Screen-reader description of what the table contains. */
  caption: string;
  /** Visible name of the horizontally scrollable region. */
  regionLabel: string;
  emptyMessage: string;
  testId: string;
  selectedKey?: string;
  onRowClick?: (row: T) => void;
  pageSize?: number;
  /** Keep dense tables readable inside their horizontally scrollable region. */
  minWidth?: number;
  /** Extra sentence under the pager, e.g. how the window was loaded. */
  pageNote?: string;
  initialSorting?: SortingState;
  /** Overrides for screens whose test contract predates this component. */
  rowTestId?: string;
  sortTestIdPrefix?: string;
  emptyTestId?: string;
};

export function DataTable<T extends RowData>({
  columns,
  rows,
  rowKey,
  caption,
  regionLabel,
  emptyMessage,
  testId,
  selectedKey,
  onRowClick,
  pageSize = 25,
  minWidth,
  pageNote,
  initialSorting = [],
  rowTestId,
  sortTestIdPrefix,
  emptyTestId,
}: DataTableProps<T>) {
  const [sorting, setSorting] = useState<SortingState>(initialSorting);
  const [pagination, setPagination] = useState<PaginationState>({ pageIndex: 0, pageSize });

  // A filter change that shrinks the result set must not strand the viewer on
  // a page that no longer exists.
  const pageCount = Math.max(1, Math.ceil(rows.length / pagination.pageSize));
  useEffect(() => {
    if (pagination.pageIndex > pageCount - 1) {
      setPagination((current) => ({ ...current, pageIndex: 0 }));
    }
  }, [pageCount, pagination.pageIndex]);

  const table = useTable({
    features: dataTableFeatures,
    columns,
    data: rows,
    state: { sorting, pagination },
    onSortingChange: setSorting,
    onPaginationChange: setPagination,
  });

  const pageRows = table.getRowModel().rows;
  const first = rows.length === 0 ? 0 : pagination.pageIndex * pagination.pageSize + 1;
  const last = Math.min(rows.length, (pagination.pageIndex + 1) * pagination.pageSize);
  const paginated = rows.length > pagination.pageSize;

  return (
    <>
      <div
        className="table-region"
        role="region"
        aria-label={regionLabel}
        tabIndex={0}
        data-testid={`${testId}-scroll`}
      >
        <table
          className={`data-table${columns.some((column) => column.meta?.width !== undefined) ? " data-table-fixed" : ""}`}
          style={minWidth === undefined ? undefined : { minWidth }}
          data-testid={testId}
        >
          <caption className="visually-hidden">{caption}</caption>
          <colgroup>
            {table.getAllLeafColumns().map((column) => (
              <col key={column.id} style={{ width: column.columnDef.meta?.width }} />
            ))}
          </colgroup>
          <thead>
            {table.getHeaderGroups().map((group) => (
              <tr key={group.id}>
                {group.headers.map((header) => {
                  const canSort = header.column.getCanSort?.() ?? false;
                  const direction = header.column.getIsSorted?.() ?? false;
                  const meta = header.column.columnDef.meta;
                  return (
                    <th
                      key={header.column.id}
                      scope="col"
                      aria-sort={canSort ? (sortLabel(direction) as "ascending" | "descending" | "none") : undefined}
                      className={meta?.numeric ? "num" : undefined}
                    >
                      {canSort ? (
                        <button
                          type="button"
                          className="column-sort"
                          data-testid={`${sortTestIdPrefix ?? `${testId}-sort`}-${header.column.id}`}
                          onClick={header.column.getToggleSortingHandler()}
                        >
                          {renderTemplate(header.column.columnDef.header, header.getContext())}
                          <span className="visually-hidden">
                            , sorted {sortLabel(direction)}, activate to change sorting
                          </span>
                          <span className="sort-indicator" aria-hidden="true">
                            <Icon
                              name={direction === "asc" ? "arrowUp" : direction === "desc" ? "arrowDown" : "sortNone"}
                              size={12}
                            />
                          </span>
                        </button>
                      ) : (
                        <span className="th-static">
                          {renderTemplate(header.column.columnDef.header, header.getContext())}
                        </span>
                      )}
                    </th>
                  );
                })}
              </tr>
            ))}
          </thead>
          <tbody>
            {pageRows.length === 0 ? (
              <tr>
                <td colSpan={columns.length} className="table-empty" data-testid={emptyTestId ?? `${testId}-empty`}>
                  {emptyMessage}
                </td>
              </tr>
            ) : (
              pageRows.map((row) => {
                const key = rowKey(row.original);
                return (
                  <tr
                    key={key}
                    className={selectedKey && key === selectedKey ? "is-selected" : undefined}
                    data-testid={rowTestId ?? `${testId}-row`}
                    onClick={onRowClick ? () => onRowClick(row.original) : undefined}
                  >
                    {row.getAllCells().map((cell) => {
                      const meta = cell.column.columnDef.meta;
                      const content = renderTemplate(cell.column.columnDef.cell, cell.getContext());
                      return meta?.rowHeader ? (
                        <th key={cell.column.id} scope="row">
                          {content}
                        </th>
                      ) : (
                        <td key={cell.column.id} className={meta?.numeric ? "num" : undefined}>
                          {content}
                        </td>
                      );
                    })}
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>
      {paginated ? (
        <div className="pagination">
          <span aria-live="polite" data-testid={`${testId}-pagination-status`}>
            Showing {first}–{last} of {rows.length}
            {pageNote ? ` ${pageNote}` : ""}
          </span>
          <div className="pagination-controls">
            <IconButton
              icon="chevronLeft"
              label="Previous page"
              bordered
              size="sm"
              disabled={!table.getCanPreviousPage()}
              data-testid={`${testId}-prev-page`}
              onClick={() => table.previousPage()}
            />
            <span className="pagination-page">
              Page {pagination.pageIndex + 1} of {pageCount}
            </span>
            <IconButton
              icon="chevronRight"
              label="Next page"
              bordered
              size="sm"
              disabled={!table.getCanNextPage()}
              data-testid={`${testId}-next-page`}
              onClick={() => table.nextPage()}
            />
          </div>
        </div>
      ) : null}
    </>
  );
}

export type SimpleColumn<T extends RowData> = {
  id: string;
  header: string;
  cell: (row: T) => ReactNode;
  rowHeader?: boolean;
  numeric?: boolean;
  width?: CSSProperties["width"];
  sortValue?: (row: T) => string | number;
};

// Most admin tables only need text cells and optional sorting, so they declare
// columns in this compact shape instead of repeating the column-helper dance.
// Callers memoise the result when the cells close over component state.
export function buildColumns<T extends RowData>(columns: Array<SimpleColumn<T>>): Array<DataColumn<T>> {
  const helper = dataColumnHelper<T>();
  return columns.map((column) =>
    helper.accessor((row: T) => (column.sortValue ? column.sortValue(row) : ""), {
      id: column.id,
      header: column.header,
      enableSorting: Boolean(column.sortValue),
      sortFn: "basic",
      meta: { rowHeader: column.rowHeader, numeric: column.numeric, width: column.width },
      cell: ({ row }) => column.cell(row.original),
    })
  ) as Array<DataColumn<T>>;
}

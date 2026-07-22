"""Exact live PostgreSQL catalog verification from immutable migration DDL."""

from __future__ import annotations

import re
from dataclasses import dataclass
from typing import Any, Iterable, Sequence

from agentnet.errors import GateBlocked


@dataclass(frozen=True, slots=True)
class ColumnSpec:
    table_name: str
    column_name: str
    data_type: str
    not_null: bool
    default_value: str | None


@dataclass(frozen=True, slots=True)
class CatalogSpec:
    tables: frozenset[str]
    columns: tuple[ColumnSpec, ...]
    constraints: tuple[tuple[str, str], ...]
    indexes: tuple[tuple[str, str], ...]


def _split_items(value: str) -> tuple[str, ...]:
    items: list[str] = []
    start = 0
    depth = 0
    quoted = False
    index = 0
    while index < len(value):
        character = value[index]
        if character == "'":
            if quoted and index + 1 < len(value) and value[index + 1] == "'":
                index += 2
                continue
            quoted = not quoted
        elif not quoted:
            if character == "(":
                depth += 1
            elif character == ")":
                depth -= 1
                if depth < 0:
                    raise ValueError("migration DDL has unbalanced parentheses")
            elif character == "," and depth == 0:
                item = value[start:index].strip()
                if item:
                    items.append(item)
                start = index + 1
        index += 1
    if quoted or depth != 0:
        raise ValueError("migration DDL is not balanced")
    final = value[start:].strip()
    if final:
        items.append(final)
    return tuple(items)


def _balanced_value(value: str, start: int) -> tuple[str, int]:
    if start >= len(value) or value[start] != "(":
        raise ValueError("expected parenthesized migration expression")
    depth = 0
    quoted = False
    index = start
    while index < len(value):
        character = value[index]
        if character == "'":
            if quoted and index + 1 < len(value) and value[index + 1] == "'":
                index += 2
                continue
            quoted = not quoted
        elif not quoted:
            if character == "(":
                depth += 1
            elif character == ")":
                depth -= 1
                if depth == 0:
                    return value[start + 1 : index], index + 1
        index += 1
    raise ValueError("migration expression is not balanced")


def _checks(value: str) -> tuple[str, ...]:
    results: list[str] = []
    cursor = 0
    while True:
        match = re.search(r"\bCHECK\b\s*", value[cursor:], re.IGNORECASE)
        if match is None:
            break
        open_at = cursor + match.end()
        while open_at < len(value) and value[open_at].isspace():
            open_at += 1
        expression, cursor = _balanced_value(value, open_at)
        results.append(f"CHECK ({expression})")
    return tuple(results)


def _outside_quotes_lower(value: str) -> str:
    rendered: list[str] = []
    quoted = False
    index = 0
    while index < len(value):
        character = value[index]
        if character == "'":
            rendered.append(character)
            if quoted and index + 1 < len(value) and value[index + 1] == "'":
                rendered.append("'")
                index += 2
                continue
            quoted = not quoted
        else:
            rendered.append(character if quoted else character.lower())
        index += 1
    if quoted:
        raise ValueError("catalog SQL contains an unterminated string")
    return "".join(rendered)


def normalize_catalog_sql(value: str) -> str:
    normalized = _outside_quotes_lower(value).replace('"', "")
    normalized = re.sub(r"::(?:text|bigint)", "", normalized)
    normalized = re.sub(r"\s+", "", normalized)
    normalized = normalized.replace("usingbtree", "")
    normalized = re.sub(
        r"(create(?:unique)?index[a-z0-9_]+on)[a-z0-9_]+\.", r"\1", normalized
    )
    normalized = re.sub(r"(references)[a-z0-9_]+\.", r"\1", normalized)
    normalized = normalized.replace("<>", "!=")
    normalized = re.sub(
        r"([a-z0-9_.()]+)=any\(array\[(.*?)\]\)",
        lambda match: f"{match.group(1)}in({match.group(2)})",
        normalized,
    )
    normalized = re.sub(
        r"([a-z0-9_.()]+)!=all\(array\[(.*?)\]\)",
        lambda match: f"{match.group(1)}notin({match.group(2)})",
        normalized,
    )
    while normalized.startswith("check((") and normalized.endswith("))"):
        normalized = "check(" + normalized[7:-2] + ")"
    return normalized


def _constraint_specs(table_name: str, item: str, column_name: str | None) -> list[tuple[str, str]]:
    constraints: list[tuple[str, str]] = []
    upper = item.upper()
    if column_name is None:
        if upper.startswith("PRIMARY KEY") or upper.startswith("UNIQUE"):
            constraints.append((table_name, normalize_catalog_sql(item)))
        elif upper.startswith("FOREIGN KEY"):
            constraints.append((table_name, normalize_catalog_sql(item)))
        constraints.extend((table_name, normalize_catalog_sql(check)) for check in _checks(item))
        return constraints

    if re.search(r"\bPRIMARY\s+KEY\b", item, re.IGNORECASE):
        constraints.append((table_name, normalize_catalog_sql(f"PRIMARY KEY ({column_name})")))
    if re.search(r"\bUNIQUE\b", item, re.IGNORECASE):
        constraints.append((table_name, normalize_catalog_sql(f"UNIQUE ({column_name})")))
    reference = re.search(
        r"\bREFERENCES\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(([^)]+)\)",
        item,
        re.IGNORECASE,
    )
    if reference is not None:
        delete_action = re.search(
            r"\bON\s+DELETE\s+(CASCADE|SET\s+NULL|RESTRICT|NO\s+ACTION)\b",
            item,
            re.IGNORECASE,
        )
        action_sql = f" ON DELETE {delete_action.group(1)}" if delete_action is not None else ""
        constraints.append(
            (
                table_name,
                normalize_catalog_sql(
                    f"FOREIGN KEY ({column_name}) REFERENCES {reference.group(1)}({reference.group(2)})"
                    f"{action_sql}"
                ),
            )
        )
    constraints.extend((table_name, normalize_catalog_sql(check)) for check in _checks(item))
    return constraints


def expected_catalog(migrations: Sequence[Any]) -> CatalogSpec:
    tables: set[str] = {"schema_migrations"}
    columns: list[ColumnSpec] = [
        ColumnSpec("schema_migrations", "version", "bigint", True, None),
        ColumnSpec("schema_migrations", "name", "text", True, None),
        ColumnSpec("schema_migrations", "checksum", "text", True, None),
        ColumnSpec("schema_migrations", "applied_at", "bigint", True, None),
    ]
    constraints: list[tuple[str, str]] = [
        ("schema_migrations", normalize_catalog_sql("PRIMARY KEY (version)"))
    ]
    indexes: list[tuple[str, str]] = []

    for migration in migrations:
        for statement in (part.strip() for part in migration.sql.split(";") if part.strip()):
            table_match = re.match(
                r"CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+([A-Za-z_][A-Za-z0-9_]*)\s*\((.*)\)\s*$",
                statement,
                re.IGNORECASE | re.DOTALL,
            )
            if table_match is not None:
                table_name = table_match.group(1)
                if table_name in tables:
                    raise ValueError(f"duplicate migration table: {table_name}")
                tables.add(table_name)
                for item in _split_items(table_match.group(2)):
                    if re.match(r"^(?:CHECK|UNIQUE|PRIMARY\s+KEY|FOREIGN\s+KEY)\b", item, re.IGNORECASE):
                        constraints.extend(_constraint_specs(table_name, item, None))
                        continue
                    column_match = re.match(
                        r"^([A-Za-z_][A-Za-z0-9_]*)\s+(TEXT|BIGINT|BIGSERIAL)\b(.*)$",
                        item,
                        re.IGNORECASE | re.DOTALL,
                    )
                    if column_match is None:
                        raise ValueError(f"unsupported migration column in {table_name}: {item[:80]}")
                    column_name = column_match.group(1)
                    declared_type = column_match.group(2).lower()
                    remainder = column_match.group(3)
                    default_match = re.search(r"\bDEFAULT\s+(-?[0-9]+)\b", remainder, re.IGNORECASE)
                    default_value = default_match.group(1) if default_match is not None else None
                    if declared_type == "bigserial":
                        default_value = f"sequence:{table_name}_{column_name}_seq"
                    columns.append(
                        ColumnSpec(
                            table_name,
                            column_name,
                            "bigint" if declared_type == "bigserial" else declared_type,
                            bool(re.search(r"\bNOT\s+NULL\b|\bPRIMARY\s+KEY\b", remainder, re.IGNORECASE)),
                            default_value,
                        )
                    )
                    constraints.extend(_constraint_specs(table_name, item, column_name))
                continue

            index_match = re.match(
                r"CREATE\s+(UNIQUE\s+)?INDEX\s+IF\s+NOT\s+EXISTS\s+([A-Za-z_][A-Za-z0-9_]*)\s+ON\s+([A-Za-z_][A-Za-z0-9_]*)\s*(\(.*)$",
                statement,
                re.IGNORECASE | re.DOTALL,
            )
            if index_match is not None:
                unique = "UNIQUE " if index_match.group(1) else ""
                definition = (
                    f"CREATE {unique}INDEX {index_match.group(2)} ON "
                    f"{index_match.group(3)}{index_match.group(4)}"
                )
                indexes.append((index_match.group(2), normalize_catalog_sql(definition)))

    return CatalogSpec(
        tables=frozenset(tables),
        columns=tuple(sorted(columns, key=lambda item: (item.table_name, item.column_name))),
        constraints=tuple(sorted(constraints)),
        indexes=tuple(sorted(indexes)),
    )


def _unquoted_qualified(value: Any) -> str | None:
    if value is None:
        return None
    return str(value).replace('"', "")


def _actual_default(row: Any) -> str | None:
    value = row["default_value"]
    owned_sequence = _unquoted_qualified(row["owned_sequence"])
    targets = tuple(
        _unquoted_qualified(target) for target in (row["sequence_targets"] or ())
    )
    if value is None:
        if owned_sequence is not None or targets:
            raise GateBlocked("schema_catalog_defaults", "PostgreSQL sequence default is not exact")
        return None
    rendered = normalize_catalog_sql(str(value))
    if not rendered.startswith("nextval("):
        if owned_sequence is not None or targets:
            raise GateBlocked("schema_catalog_defaults", "PostgreSQL sequence default is not exact")
        return rendered
    expected_name = f"{row['current_schema_name']}.{row['table_name']}_{row['column_name']}_seq"
    if owned_sequence != expected_name or targets != (expected_name,):
        raise GateBlocked("schema_catalog_defaults", "PostgreSQL sequence default is not exact")
    return f"sequence:{row['table_name']}_{row['column_name']}_seq"


def require_exact_postgres_catalog(
    connection: Any,
    *,
    migrations: Sequence[Any],
) -> None:
    """Compare full live tables, columns, constraints, and non-constraint indexes."""

    expected = expected_catalog(migrations)
    table_rows = connection.execute(
        """SELECT relation.relname AS table_name,relation.relkind AS kind
             FROM pg_catalog.pg_class relation
             JOIN pg_catalog.pg_namespace namespace ON namespace.oid=relation.relnamespace
            WHERE namespace.nspname=current_schema()
              AND relation.relkind IN ('r','p','v','m','f')
            ORDER BY relation.relname"""
    ).fetchall()
    actual_tables = {
        str(row["table_name"])
        for row in table_rows
        if str(row["kind"]) == "r"
    }
    if actual_tables != set(expected.tables) or any(str(row["kind"]) != "r" for row in table_rows):
        raise GateBlocked("schema_catalog_tables", "PostgreSQL table catalog is not exact")

    column_rows = connection.execute(
        """SELECT relation.relname AS table_name,attribute.attname AS column_name,
                  pg_catalog.format_type(attribute.atttypid,attribute.atttypmod) AS data_type,
                  attribute.attnotnull AS not_null,current_schema() AS current_schema_name,
                  pg_catalog.pg_get_expr(default_value.adbin,default_value.adrelid,true) AS default_value,
                  pg_catalog.pg_get_serial_sequence(
                      pg_catalog.format('%I.%I',namespace.nspname,relation.relname),
                      attribute.attname
                  ) AS owned_sequence,
                  sequence_dependency.sequence_targets AS sequence_targets
             FROM pg_catalog.pg_attribute attribute
             JOIN pg_catalog.pg_class relation ON relation.oid=attribute.attrelid
             JOIN pg_catalog.pg_namespace namespace ON namespace.oid=relation.relnamespace
             LEFT JOIN pg_catalog.pg_attrdef default_value
               ON default_value.adrelid=relation.oid AND default_value.adnum=attribute.attnum
             LEFT JOIN LATERAL (
                 SELECT ARRAY_AGG(
                     sequence_namespace.nspname || '.' || sequence_relation.relname
                     ORDER BY sequence_namespace.nspname,sequence_relation.relname
                 ) AS sequence_targets
                   FROM pg_catalog.pg_depend dependency
                   JOIN pg_catalog.pg_class sequence_relation
                     ON sequence_relation.oid=dependency.refobjid
                    AND sequence_relation.relkind='S'
                   JOIN pg_catalog.pg_namespace sequence_namespace
                     ON sequence_namespace.oid=sequence_relation.relnamespace
                  WHERE default_value.oid IS NOT NULL
                    AND dependency.classid='pg_attrdef'::regclass
                    AND dependency.objid=default_value.oid
                    AND dependency.refclassid='pg_class'::regclass
             ) sequence_dependency ON TRUE
            WHERE namespace.nspname=current_schema() AND relation.relname=ANY(%s)
              AND attribute.attnum>0 AND NOT attribute.attisdropped
            ORDER BY relation.relname,attribute.attname""",
        (sorted(expected.tables),),
    ).fetchall()
    actual_columns = tuple(
        sorted(
            (
                ColumnSpec(
                    str(row["table_name"]),
                    str(row["column_name"]),
                    str(row["data_type"]).lower(),
                    bool(row["not_null"]),
                    _actual_default(row),
                )
                for row in column_rows
            ),
            key=lambda item: (item.table_name, item.column_name),
        )
    )
    if actual_columns != expected.columns:
        raise GateBlocked("schema_catalog_columns", "PostgreSQL column catalog is not exact")

    constraint_rows = connection.execute(
        """SELECT relation.relname AS table_name,constraint_row.contype AS constraint_type,
                  current_schema() AS current_schema_name,
                  referenced_namespace.nspname AS referenced_schema,
                  pg_catalog.pg_get_constraintdef(constraint_row.oid,true) AS definition
             FROM pg_catalog.pg_constraint constraint_row
             JOIN pg_catalog.pg_class relation ON relation.oid=constraint_row.conrelid
             JOIN pg_catalog.pg_namespace namespace ON namespace.oid=relation.relnamespace
             LEFT JOIN pg_catalog.pg_class referenced_relation
               ON referenced_relation.oid=constraint_row.confrelid
             LEFT JOIN pg_catalog.pg_namespace referenced_namespace
               ON referenced_namespace.oid=referenced_relation.relnamespace
            WHERE namespace.nspname=current_schema() AND relation.relname=ANY(%s)
            ORDER BY relation.relname,constraint_row.oid""",
        (sorted(expected.tables),),
    ).fetchall()
    for row in constraint_rows:
        if (
            str(row["constraint_type"]) == "f"
            and str(row["referenced_schema"]) != str(row["current_schema_name"])
        ):
            raise GateBlocked("schema_catalog_constraints", "PostgreSQL FK target schema is not exact")
    actual_constraints = tuple(
        sorted(
            (str(row["table_name"]), normalize_catalog_sql(str(row["definition"])))
            for row in constraint_rows
        )
    )
    if actual_constraints != expected.constraints:
        raise GateBlocked("schema_catalog_constraints", "PostgreSQL constraint catalog is not exact")

    index_rows = connection.execute(
        """SELECT index_relation.relname AS index_name,
                  pg_catalog.pg_get_indexdef(index_relation.oid) AS definition
             FROM pg_catalog.pg_index index_row
             JOIN pg_catalog.pg_class index_relation ON index_relation.oid=index_row.indexrelid
             JOIN pg_catalog.pg_class table_relation ON table_relation.oid=index_row.indrelid
             JOIN pg_catalog.pg_namespace namespace ON namespace.oid=table_relation.relnamespace
             LEFT JOIN pg_catalog.pg_constraint constraint_row
               ON constraint_row.conindid=index_relation.oid
            WHERE namespace.nspname=current_schema() AND constraint_row.oid IS NULL
              AND table_relation.relname=ANY(%s)
            ORDER BY index_relation.relname""",
        (sorted(expected.tables),),
    ).fetchall()
    actual_indexes = tuple(
        sorted(
            (str(row["index_name"]), normalize_catalog_sql(str(row["definition"])))
            for row in index_rows
        )
    )
    if actual_indexes != expected.indexes:
        raise GateBlocked("schema_catalog_indexes", "PostgreSQL index catalog is not exact")


__all__ = [
    "CatalogSpec",
    "ColumnSpec",
    "expected_catalog",
    "normalize_catalog_sql",
    "require_exact_postgres_catalog",
]

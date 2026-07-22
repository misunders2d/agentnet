"""Exact live PostgreSQL catalog verification from immutable migration DDL."""

from __future__ import annotations

import hashlib
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
class ConstraintSpec:
    table_name: str
    constraint_type: str
    definition: str


@dataclass(frozen=True, slots=True)
class CatalogSpec:
    tables: frozenset[str]
    columns: tuple[ColumnSpec, ...]
    constraints: tuple[ConstraintSpec, ...]
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


_CHECK_KEYWORDS = {
    "all",
    "and",
    "any",
    "array",
    "between",
    "false",
    "in",
    "is",
    "not",
    "null",
    "or",
    "true",
}
_ALLOWED_CHECK_FUNCTIONS = {"length"}
_ALLOWED_CHECK_CASTS = {"bigint", "text"}


def _check_tokens(value: str) -> tuple[tuple[str, str], ...]:
    tokens: list[tuple[str, str]] = []
    index = 0
    while index < len(value):
        character = value[index]
        if character.isspace():
            index += 1
            continue
        if value.startswith("--", index) or value.startswith("/*", index):
            raise ValueError("catalog CHECK comments are unsupported")
        if character == "'":
            rendered: list[str] = []
            index += 1
            while index < len(value):
                if value[index] == "'":
                    if index + 1 < len(value) and value[index + 1] == "'":
                        rendered.append("'")
                        index += 2
                        continue
                    index += 1
                    break
                rendered.append(value[index])
                index += 1
            else:
                raise ValueError("catalog CHECK string is unterminated")
            tokens.append(("string", "".join(rendered)))
            continue
        if character == '"':
            rendered = []
            index += 1
            while index < len(value):
                if value[index] == '"':
                    if index + 1 < len(value) and value[index + 1] == '"':
                        rendered.append('"')
                        index += 2
                        continue
                    index += 1
                    break
                rendered.append(value[index])
                index += 1
            else:
                raise ValueError("catalog CHECK identifier is unterminated")
            identifier = "".join(rendered)
            if re.fullmatch(r"[a-z_][a-z0-9_]*", identifier):
                tokens.append(("identifier", identifier))
            else:
                tokens.append(("quoted_identifier", identifier))
            continue
        number = re.match(r"-?[0-9]+", value[index:])
        if number is not None:
            tokens.append(("number", number.group(0)))
            index += len(number.group(0))
            continue
        identifier = re.match(r"[A-Za-z_][A-Za-z0-9_]*", value[index:])
        if identifier is not None:
            rendered = identifier.group(0).lower()
            tokens.append(("keyword" if rendered in _CHECK_KEYWORDS else "identifier", rendered))
            index += len(identifier.group(0))
            continue
        operator = next(
            (candidate for candidate in ("::", ">=", "<=", "<>", "!=", "||", "=", ">", "<") if value.startswith(candidate, index)),
            None,
        )
        if operator is not None:
            tokens.append(("operator", operator))
            index += len(operator)
            continue
        if character in "(),[]":
            tokens.append(("punctuation", character))
            index += 1
            continue
        raise ValueError(f"unsupported catalog CHECK token at offset {index}")
    return tuple(tokens)


def _boolean_node(operator: str, *values: Any) -> tuple[Any, ...]:
    flattened: list[Any] = []
    for value in values:
        if isinstance(value, tuple) and value and value[0] == operator:
            flattened.extend(value[1:])
        else:
            flattened.append(value)
    return (operator, *flattened)


class _CheckParser:
    def __init__(self, tokens: tuple[tuple[str, str], ...]):
        self._tokens = tokens
        self._index = 0

    def parse(self) -> Any:
        if not self._tokens:
            raise ValueError("catalog CHECK expression is empty")
        result = self._parse_or()
        if self._index != len(self._tokens):
            raise ValueError("catalog CHECK expression has trailing tokens")
        return result

    def _peek(self, value: str | None = None) -> tuple[str, str] | None:
        if self._index >= len(self._tokens):
            return None
        token = self._tokens[self._index]
        if value is not None and token[1] != value:
            return None
        return token

    def _take(self, value: str | None = None) -> tuple[str, str]:
        token = self._peek(value)
        if token is None:
            expected = value or "token"
            raise ValueError(f"catalog CHECK expected {expected}")
        self._index += 1
        return token

    def _parse_or(self) -> Any:
        result = self._parse_and()
        while self._peek("or") is not None:
            self._take("or")
            result = _boolean_node("or", result, self._parse_and())
        return result

    def _parse_and(self) -> Any:
        result = self._parse_not()
        while self._peek("and") is not None:
            self._take("and")
            result = _boolean_node("and", result, self._parse_not())
        return result

    def _parse_not(self) -> Any:
        if self._peek("not") is not None:
            self._take("not")
            return ("not", self._parse_not())
        return self._parse_predicate()

    def _parse_predicate(self) -> Any:
        left = self._parse_concat()
        token = self._peek()
        if token is None:
            return left
        if token == ("keyword", "is"):
            self._take("is")
            negated = self._peek("not") is not None
            if negated:
                self._take("not")
            self._take("null")
            return ("is_not_null" if negated else "is_null", left)
        negated = False
        if token == ("keyword", "not"):
            self._take("not")
            negated = True
            token = self._peek()
        if token == ("keyword", "in"):
            self._take("in")
            values = self._parse_parenthesized_values()
            return ("not_in" if negated else "in", left, values)
        if token == ("keyword", "between"):
            if negated:
                raise ValueError("catalog CHECK NOT BETWEEN is unsupported")
            self._take("between")
            lower = self._parse_concat()
            self._take("and")
            upper = self._parse_concat()
            return _boolean_node(
                "and",
                ("compare", ">=", left, lower),
                ("compare", "<=", left, upper),
            )
        if negated:
            raise ValueError("catalog CHECK NOT must precede IN or a predicate")
        if token[0] == "operator" and token[1] in {"=", "!=", "<>", ">", ">=", "<", "<="}:
            operator = self._take()[1]
            if self._peek("any") is not None or self._peek("all") is not None:
                quantifier = self._take()[1]
                self._take("(")
                values = self._parse_array()
                self._take(")")
                if operator == "=" and quantifier == "any":
                    return ("in", left, values)
                if operator in {"!=", "<>"} and quantifier == "all":
                    return ("not_in", left, values)
                raise ValueError("unsupported catalog CHECK quantified comparison")
            right = self._parse_concat()
            return ("compare", "!=" if operator == "<>" else operator, left, right)
        return left

    def _parse_concat(self) -> Any:
        result = self._parse_primary()
        while self._peek("||") is not None:
            self._take("||")
            result = ("concat", result, self._parse_primary())
        return result

    def _parse_primary(self) -> Any:
        token = self._peek()
        if token is None:
            raise ValueError("catalog CHECK value is missing")
        if token == ("punctuation", "("):
            self._take("(")
            result = self._parse_or()
            self._take(")")
        elif token[0] in {"identifier", "quoted_identifier"}:
            self._take()
            if self._peek("(") is not None:
                if token[1] not in _ALLOWED_CHECK_FUNCTIONS:
                    raise ValueError("unsupported catalog CHECK function")
                self._take("(")
                arguments: list[Any] = []
                if self._peek(")") is None:
                    arguments.append(self._parse_or())
                    while self._peek(",") is not None:
                        self._take(",")
                        arguments.append(self._parse_or())
                self._take(")")
                result = ("call", token[1], tuple(arguments))
            else:
                result = (token[0], token[1])
        elif token[0] in {"number", "string"}:
            self._take()
            result = (token[0], token[1])
        elif token[0] == "keyword" and token[1] in {"null", "true", "false"}:
            self._take()
            result = ("literal", token[1])
        elif token == ("keyword", "array"):
            result = ("array", self._parse_array())
        else:
            raise ValueError("unsupported catalog CHECK value")
        while self._peek("::") is not None:
            self._take("::")
            cast = self._take()[1]
            if cast not in _ALLOWED_CHECK_CASTS:
                raise ValueError("unsupported catalog CHECK cast")
            array_cast = self._peek("[") is not None
            if array_cast:
                self._take("[")
                self._take("]")
            if result[0] not in {"number", "string", "literal", "array"}:
                result = ("cast", cast + ("[]" if array_cast else ""), result)
        return result

    def _parse_array(self) -> tuple[Any, ...]:
        self._take("array")
        self._take("[")
        values: list[Any] = []
        if self._peek("]") is None:
            values.append(self._parse_concat())
            while self._peek(",") is not None:
                self._take(",")
                values.append(self._parse_concat())
        self._take("]")
        return tuple(values)

    def _parse_parenthesized_values(self) -> tuple[Any, ...]:
        self._take("(")
        values: list[Any] = []
        if self._peek(")") is None:
            values.append(self._parse_concat())
            while self._peek(",") is not None:
                self._take(",")
                values.append(self._parse_concat())
        self._take(")")
        return tuple(values)


def _canonical_expression(value: str) -> Any:
    return _CheckParser(_check_tokens(value)).parse()


def _canonical_check_sql(value: str) -> str:
    match = re.fullmatch(r"\s*CHECK\s*\((.*)\)\s*", value, re.IGNORECASE | re.DOTALL)
    if match is None:
        raise ValueError("catalog CHECK definition is malformed")
    return f"check:{_canonical_expression(match.group(1))!r}"


def _canonical_index_sql(value: str) -> str:
    match = re.fullmatch(
        r"\s*CREATE\s+(UNIQUE\s+)?INDEX\s+([A-Za-z_][A-Za-z0-9_]*)\s+"
        r"ON\s+(?:[A-Za-z_][A-Za-z0-9_]*\.)?([A-Za-z_][A-Za-z0-9_]*)\s*"
        r"(?:USING\s+([A-Za-z_][A-Za-z0-9_]*)\s*)?\(([^()]*)\)"
        r"(?:\s+WHERE\s+(.+))?\s*",
        value,
        re.IGNORECASE | re.DOTALL,
    )
    if match is None:
        raise ValueError("catalog index definition is unsupported")
    method = (match.group(4) or "btree").lower()
    if method != "btree":
        raise ValueError("catalog index method is unsupported")
    columns = tuple(part.strip().replace('"', "").lower() for part in match.group(5).split(","))
    if not columns or any(not re.fullmatch(r"[a-z_][a-z0-9_]*", column) for column in columns):
        raise ValueError("catalog index columns are unsupported")
    predicate = _canonical_expression(match.group(6)) if match.group(6) is not None else None
    return (
        "index:"
        f"{(bool(match.group(1)), match.group(2).lower(), match.group(3).lower(), columns, predicate)!r}"
    )


def normalize_catalog_sql(value: str) -> str:
    if value.startswith(("check:", "index:")):
        return value
    if re.match(r"\s*CHECK\b", value, re.IGNORECASE):
        return _canonical_check_sql(value)
    if re.match(r"\s*CREATE\s+(?:UNIQUE\s+)?INDEX\b", value, re.IGNORECASE):
        return _canonical_index_sql(value)
    normalized = _outside_quotes_lower(value).replace('"', "")
    normalized = re.sub(r"::(?:text|bigint)", "", normalized)
    normalized = re.sub(r"\s+", "", normalized)
    normalized = normalized.replace("usingbtree", "")
    normalized = re.sub(
        r"(create(?:unique)?index[a-z0-9_]+on)[a-z0-9_]+\.", r"\1", normalized
    )
    normalized = re.sub(r"(references)[a-z0-9_]+\.", r"\1", normalized)
    normalized = normalized.replace("<>", "!=")
    return normalized


def _constraint_specs(table_name: str, item: str, column_name: str | None) -> list[ConstraintSpec]:
    constraints: list[ConstraintSpec] = []
    if column_name is None:
        identifier_list = r"\s*[A-Za-z_][A-Za-z0-9_]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_]*)*\s*"
        if re.fullmatch(rf"PRIMARY\s+KEY\s*\({identifier_list}\)\s*", item, re.IGNORECASE):
            return [ConstraintSpec(table_name, "p", normalize_catalog_sql(item))]
        if re.fullmatch(rf"UNIQUE\s*\({identifier_list}\)\s*", item, re.IGNORECASE):
            return [ConstraintSpec(table_name, "u", normalize_catalog_sql(item))]
        if re.fullmatch(
            rf"FOREIGN\s+KEY\s*\({identifier_list}\)\s+REFERENCES\s+"
            rf"[A-Za-z_][A-Za-z0-9_]*\s*\({identifier_list}\)"
            rf"(?:\s+ON\s+DELETE\s+(?:CASCADE|SET\s+NULL|RESTRICT|NO\s+ACTION))?\s*",
            item,
            re.IGNORECASE,
        ):
            return [ConstraintSpec(table_name, "f", normalize_catalog_sql(item))]
        checks = _checks(item)
        if len(checks) == 1 and not _without_check_clauses(item).strip():
            return [ConstraintSpec(table_name, "c", normalize_catalog_sql(checks[0]))]
        raise ValueError(f"unsupported migration table constraint in {table_name}")

    if re.search(r"\bPRIMARY\s+KEY\b", item, re.IGNORECASE):
        constraints.append(
            ConstraintSpec(table_name, "p", normalize_catalog_sql(f"PRIMARY KEY ({column_name})"))
        )
    if re.search(r"\bUNIQUE\b", item, re.IGNORECASE):
        constraints.append(
            ConstraintSpec(table_name, "u", normalize_catalog_sql(f"UNIQUE ({column_name})"))
        )
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
            ConstraintSpec(
                table_name,
                "f",
                normalize_catalog_sql(
                    f"FOREIGN KEY ({column_name}) REFERENCES {reference.group(1)}({reference.group(2)})"
                    f"{action_sql}"
                ),
            )
        )
    constraints.extend(
        ConstraintSpec(table_name, "c", normalize_catalog_sql(check)) for check in _checks(item)
    )
    return constraints


def _without_check_clauses(value: str) -> str:
    rendered: list[str] = []
    cursor = 0
    while True:
        match = re.search(r"\bCHECK\b", value[cursor:], re.IGNORECASE)
        if match is None:
            rendered.append(value[cursor:])
            return "".join(rendered)
        check_at = cursor + match.start()
        rendered.append(value[cursor:check_at])
        open_at = cursor + match.end()
        while open_at < len(value) and value[open_at].isspace():
            open_at += 1
        _, cursor = _balanced_value(value, open_at)


def _validate_column_remainder(table_name: str, column_name: str, value: str) -> None:
    residue = _without_check_clauses(value)
    supported = (
        r"\bREFERENCES\s+[A-Za-z_][A-Za-z0-9_]*\s*\([^)]*\)"
        r"(?:\s+ON\s+DELETE\s+(?:CASCADE|SET\s+NULL|RESTRICT|NO\s+ACTION))?",
        r"\bPRIMARY\s+KEY\b",
        r"\bNOT\s+NULL\b",
        r"\bUNIQUE\b",
        r"\bDEFAULT\s+-?[0-9]+\b",
    )
    for pattern in supported:
        residue = re.sub(pattern, " ", residue, flags=re.IGNORECASE)
    if residue.strip():
        raise ValueError(
            f"unsupported migration column attributes in {table_name}.{column_name}"
        )


_CATALOG_NEUTRAL_MIGRATION_DML_SHA256 = frozenset(
    {
        "4f623c44307190b67adf027fbfdd731938a09a741cb6f1b94aace7f50e0dbe25",
        "047548f1f9be8e18b89248ceb1f4c64ac07d8264fbb23b62828ddb8d9f85e142",
        "05dba4c68a3bf3180a80983ff79cee5a9592e75f946b27c1959db2c797605596",
    }
)


def _catalog_statement_body(value: str) -> str:
    cursor = 0
    while True:
        whitespace = re.match(r"\s*", value[cursor:])
        cursor += whitespace.end() if whitespace is not None else 0
        if value.startswith("--", cursor):
            newline = value.find("\n", cursor + 2)
            if newline < 0:
                return ""
            cursor = newline + 1
            continue
        if value.startswith("/*", cursor):
            close = value.find("*/", cursor + 2)
            if close < 0:
                raise ValueError("migration statement contains unterminated comment")
            cursor = close + 2
            continue
        return value[cursor:].strip()


def expected_catalog(migrations: Sequence[Any]) -> CatalogSpec:
    tables: set[str] = {"schema_migrations"}
    columns: list[ColumnSpec] = [
        ColumnSpec("schema_migrations", "version", "bigint", True, None),
        ColumnSpec("schema_migrations", "name", "text", True, None),
        ColumnSpec("schema_migrations", "checksum", "text", True, None),
        ColumnSpec("schema_migrations", "applied_at", "bigint", True, None),
    ]
    constraints: list[ConstraintSpec] = [
        ConstraintSpec("schema_migrations", "p", normalize_catalog_sql("PRIMARY KEY (version)"))
    ]
    indexes: list[tuple[str, str]] = []

    for migration in migrations:
        for raw_statement in (part.strip() for part in migration.sql.split(";") if part.strip()):
            statement = _catalog_statement_body(raw_statement)
            if not statement:
                continue
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
                table_column_start = len(columns)
                table_primary_key_columns: set[str] = set()
                for item in _split_items(table_match.group(2)):
                    if re.match(r"^(?:CHECK|UNIQUE|PRIMARY\s+KEY|FOREIGN\s+KEY)\b", item, re.IGNORECASE):
                        constraints.extend(_constraint_specs(table_name, item, None))
                        primary_key = re.match(
                            r"^PRIMARY\s+KEY\s*\(([^)]+)\)", item, re.IGNORECASE | re.DOTALL
                        )
                        if primary_key is not None:
                            table_primary_key_columns.update(
                                name.strip().replace('"', "")
                                for name in primary_key.group(1).split(",")
                            )
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
                    _validate_column_remainder(table_name, column_name, remainder)
                    default_match = re.search(r"\bDEFAULT\s+(-?[0-9]+)\b", remainder, re.IGNORECASE)
                    default_value = default_match.group(1) if default_match is not None else None
                    if declared_type == "bigserial":
                        if default_match is not None:
                            raise ValueError(f"explicit BIGSERIAL default in {table_name}.{column_name}")
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
                if table_primary_key_columns:
                    known_columns = {
                        column.column_name for column in columns[table_column_start:]
                    }
                    if not table_primary_key_columns <= known_columns:
                        raise ValueError(f"primary key names unknown column in {table_name}")
                    for index in range(table_column_start, len(columns)):
                        column = columns[index]
                        if column.column_name in table_primary_key_columns and not column.not_null:
                            columns[index] = ColumnSpec(
                                column.table_name,
                                column.column_name,
                                column.data_type,
                                True,
                                column.default_value,
                            )
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
                continue

            statement_digest = hashlib.sha256(statement.encode("utf-8")).hexdigest()
            if statement_digest in _CATALOG_NEUTRAL_MIGRATION_DML_SHA256:
                continue
            raise ValueError(f"unsupported migration statement: {statement[:80]}")

    return CatalogSpec(
        tables=frozenset(tables),
        columns=tuple(sorted(columns, key=lambda item: (item.table_name, item.column_name))),
        constraints=tuple(
            sorted(
                constraints,
                key=lambda item: (item.table_name, item.constraint_type, item.definition),
            )
        ),
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
        """SELECT relation.relname AS table_name,relation.relkind AS kind,
                  relation.relpersistence AS persistence
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
    if actual_tables != set(expected.tables) or any(
        str(row["kind"]) != "r" or str(row["persistence"]) != "p"
        for row in table_rows
    ):
        raise GateBlocked("schema_catalog_tables", "PostgreSQL table catalog is not exact")

    column_rows = connection.execute(
        """SELECT relation.relname AS table_name,attribute.attname AS column_name,
                  pg_catalog.format_type(attribute.atttypid,attribute.atttypmod) AS data_type,
                  attribute.attnotnull AS not_null,current_schema() AS current_schema_name,
                  pg_catalog.pg_get_expr(default_value.adbin,default_value.adrelid,true) AS default_value,
                  pg_catalog.pg_get_serial_sequence(
                      pg_catalog.quote_ident(namespace.nspname) || '.' ||
                      pg_catalog.quote_ident(relation.relname),
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
                  constraint_row.confrelid AS referenced_relation_oid,
                  referenced_namespace.nspname AS referenced_schema,
                  constraint_row.convalidated AS validated,
                  constraint_row.coninhcount AS inheritance_count,
                  constraint_columns.column_names AS constrained_columns,
                  pg_catalog.pg_get_constraintdef(constraint_row.oid,true) AS definition
             FROM pg_catalog.pg_constraint constraint_row
             JOIN pg_catalog.pg_class relation ON relation.oid=constraint_row.conrelid
             JOIN pg_catalog.pg_namespace namespace ON namespace.oid=relation.relnamespace
             LEFT JOIN pg_catalog.pg_class referenced_relation
               ON referenced_relation.oid=constraint_row.confrelid
             LEFT JOIN pg_catalog.pg_namespace referenced_namespace
               ON referenced_namespace.oid=referenced_relation.relnamespace
             LEFT JOIN LATERAL (
                 SELECT ARRAY_AGG(
                            constrained_attribute.attname ORDER BY key_position.ordinality
                        ) AS column_names
                   FROM unnest(constraint_row.conkey) WITH ORDINALITY
                        AS key_position(attnum,ordinality)
                   LEFT JOIN pg_catalog.pg_attribute constrained_attribute
                     ON constrained_attribute.attrelid=relation.oid
                    AND constrained_attribute.attnum=key_position.attnum
                    AND constrained_attribute.attnum>0
                    AND NOT constrained_attribute.attisdropped
             ) constraint_columns ON TRUE
            WHERE namespace.nspname=current_schema() AND relation.relname=ANY(%s)
            ORDER BY relation.relname,constraint_row.oid""",
        (sorted(expected.tables),),
    ).fetchall()
    expected_not_null = tuple(
        sorted(
            (column.table_name, column.column_name)
            for column in expected.columns
            if column.not_null
        )
    )
    actual_not_null: list[tuple[str, str]] = []
    actual_constraints: list[ConstraintSpec] = []
    for row in constraint_rows:
        constraint_type = str(row["constraint_type"])
        table_name = str(row["table_name"])
        if constraint_type not in {"c", "f", "n", "p", "u"}:
            raise GateBlocked(
                "schema_catalog_constraints",
                "PostgreSQL constraint type is not exact",
            )
        if constraint_type == "n":
            constrained_columns = tuple(row["constrained_columns"] or ())
            if (
                not bool(row["validated"])
                or int(row["inheritance_count"]) != 0
                or int(row["referenced_relation_oid"]) != 0
                or row["referenced_schema"] is not None
                or len(constrained_columns) != 1
                or constrained_columns[0] is None
                or not str(constrained_columns[0])
            ):
                raise GateBlocked(
                    "schema_catalog_constraints",
                    "PostgreSQL NOT NULL constraint catalog is not exact",
                )
            actual_not_null.append((table_name, str(constrained_columns[0])))
            continue
        if not bool(row["validated"]) or int(row["inheritance_count"]) != 0:
            raise GateBlocked(
                "schema_catalog_constraints",
                "PostgreSQL constraint validation is not exact",
            )
        if (
            constraint_type == "f"
            and str(row["referenced_schema"]) != str(row["current_schema_name"])
        ):
            raise GateBlocked("schema_catalog_constraints", "PostgreSQL FK target schema is not exact")
        try:
            definition = normalize_catalog_sql(str(row["definition"]))
        except ValueError as exc:
            raise GateBlocked(
                "schema_catalog_constraints",
                "PostgreSQL constraint definition is unsupported",
            ) from exc
        actual_constraints.append(
            ConstraintSpec(table_name, constraint_type, definition)
        )
    if tuple(sorted(actual_not_null)) != expected_not_null:
        raise GateBlocked(
            "schema_catalog_constraints",
            "PostgreSQL NOT NULL constraint catalog is not exact",
        )
    actual_constraint_tuple = tuple(
        sorted(
            actual_constraints,
            key=lambda item: (item.table_name, item.constraint_type, item.definition),
        )
    )
    if actual_constraint_tuple != expected.constraints:
        raise GateBlocked("schema_catalog_constraints", "PostgreSQL constraint catalog is not exact")

    index_rows = connection.execute(
        """SELECT index_relation.relname AS index_name,
                  pg_catalog.pg_get_indexdef(index_relation.oid) AS definition,
                  index_row.indisvalid AS is_valid,index_row.indisready AS is_ready,
                  index_row.indislive AS is_live
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
    if any(
        not bool(row["is_valid"])
        or not bool(row["is_ready"])
        or not bool(row["is_live"])
        for row in index_rows
    ):
        raise GateBlocked("schema_catalog_indexes", "PostgreSQL index state is not exact")
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
    "ConstraintSpec",
    "expected_catalog",
    "normalize_catalog_sql",
    "require_exact_postgres_catalog",
]

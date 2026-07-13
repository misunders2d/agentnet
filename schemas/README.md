# Protocol schemas

`v1/*.json` is generated from the strict Pydantic contract catalog by:

```bash
PYTHONPATH=src .venv/bin/python scripts/export_schemas.py
```

Unknown fields are rejected at trust boundaries. Unknown major versions and
unknown critical extensions fail closed. Generated schemas describe canonical
corporate objects; they do not make an external component authoritative.


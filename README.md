## Usage

```yaml
version: "2"
plugins:
  - name: py
    wasm:
      url: https://downloads.sqlc.dev/plugin/sqlc-gen-python_1.3.0.wasm
      sha256: fbedae96b5ecae2380a70fb5b925fd4bff58a6cfb1f3140375d098fbab7b3a3c
sql:
  - schema: "schema.sql"
    queries: "query.sql"
    engine: postgresql
    codegen:
      - out: src/authors
        plugin: py
        options:
          package: authors
          emit_sync_querier: true
          emit_async_querier: true
```

### Drivers and SQLite

Option: `driver`

The default driver is `sqlalchemy`, which preserves the existing PostgreSQL
output and accepts SQLAlchemy `Connection` and `AsyncConnection` objects.

Set `driver: dbapi` for SQLite connections that execute raw SQL with qmark
parameters:

```yaml
sql:
  - schema: "schema.sql"
    queries: "query.sql"
    engine: sqlite
    codegen:
      - out: src/records
        plugin: py
        options:
          package: records
          driver: dbapi
          emit_async_querier: true
```

The generated async querier accepts any object that structurally implements:

```py
class AsyncConnection:
    async def execute(
        self,
        sql: str,
        parameters: tuple[Any, ...],
        /,
    ) -> MaterializedCursor: ...

class MaterializedCursor:
    @property
    def rowcount(self) -> int: ...

    def fetchone(self) -> Sequence[Any] | None: ...
    def fetchall(self) -> Sequence[Sequence[Any]]: ...
```

With `emit_sync_querier`, the equivalent connection has a synchronous
`execute` method. Cursor fetches are synchronous in both modes because the
cursor is expected to be materialized by `execute`. Generated methods never
commit or roll back; transaction ownership remains with the caller.
The connection arguments are positional-only, so implementations may use
different parameter names. `rowcount` is a read-only property, matching
standard SQLite cursor implementations.

SQLite numbered parameters are emitted as positional `?` placeholders and
passed as tuples. Repeated placeholders repeat the value in the tuple while
remaining one argument in the generated method. SQLite declared types use
affinity-based annotations:

| SQLite affinity | Python annotation |
|-----------------|-------------------|
| INTEGER | `int` |
| TEXT | `str` |
| REAL | `float` |
| explicitly declared BLOB | `bytes` |
| NUMERIC, undeclared, or unknown | `Any` |

Nullability is represented with `Optional` as it is for PostgreSQL.
For `:execrows` statements with `RETURNING`, generated methods consume the
returned rows before reading `rowcount`, as required by Python's `sqlite3`.
`driver: dbapi` currently requires `engine: sqlite`; unsupported combinations
fail generation with a descriptive error.

### Emit Pydantic Models instead of `dataclasses`

Option: `emit_pydantic_models`

By default, `sqlc-gen-python` will emit `dataclasses` for the models. If you prefer to use [`pydantic`](https://docs.pydantic.dev/latest/) models, you can enable this option.

with `emit_pydantic_models`

```py
from pydantic import BaseModel

class Author(pydantic.BaseModel):
    id: int
    name: str
```

without `emit_pydantic_models`

```py
import dataclasses

@dataclasses.dataclass()
class Author:
    id: int
    name: str
```

### Use `enum.StrEnum` for Enums

Option: `emit_str_enum`

`enum.StrEnum` was introduce in Python 3.11.

`enum.StrEnum` is a subclass of `str` that is also a subclass of `Enum`. This allows for the use of `Enum` values as strings, compared to strings, or compared to other `enum.StrEnum` types.

This is convenient for type checking and validation, as well as for serialization and deserialization.

By default, `sqlc-gen-python` will emit `(str, enum.Enum)` for the enum classes. If you prefer to use `enum.StrEnum`, you can enable this option.

with `emit_str_enum`

```py
class Status(enum.StrEnum):
    """Venues can be either open or closed"""
    OPEN = "op!en"
    CLOSED = "clo@sed"
```

without `emit_str_enum` (current behavior)

```py
class Status(str, enum.Enum):
    """Venues can be either open or closed"""
    OPEN = "op!en"
    CLOSED = "clo@sed"
```

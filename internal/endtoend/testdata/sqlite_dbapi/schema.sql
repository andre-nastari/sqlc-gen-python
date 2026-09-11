CREATE TABLE records (
    id INTEGER PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    note TEXT,
    payload BLOB NOT NULL,
    score REAL,
    dynamic_value NUMERIC
);
